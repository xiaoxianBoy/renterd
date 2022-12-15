package stores

import (
	"bytes"
	"encoding/gob"
	"errors"
	"fmt"
	"math/big"
	"time"

	"go.sia.tech/siad/crypto"

	"go.sia.tech/renterd/bus"
	"go.sia.tech/renterd/internal/consensus"
	rhpv2 "go.sia.tech/renterd/rhp/v2"
	"go.sia.tech/siad/types"
	"gorm.io/gorm"
)

const archivalReasonRenewed = "renewed"

var (
	// ErrContractNotFound is returned when a contract can't be retrieved from the
	// database.
	ErrContractNotFound = errors.New("couldn't find contract")

	// ErrContractSetNotFound is returned when a contract can't be retrieved from the
	// database.
	ErrContractSetNotFound = errors.New("couldn't find contract set")
)

type (
	dbContract struct {
		Model

		FCID        types.FileContractID `gorm:"unique;index,type:bytes;serializer:gob;NOT NULL;column:fcid"`
		HostID      uint                 `gorm:"index"`
		Host        dbHost
		LockedUntil time.Time
		RenewedFrom types.FileContractID   `gorm:"index,type:bytes;serializer:gob"`
		Revision    dbFileContractRevision `gorm:"constraint:OnDelete:CASCADE;NOT NULL"` // CASCADE to delete revision too
		StartHeight uint64                 `gorm:"NOT NULL"`
		TotalCost   *big.Int               `gorm:"type:bytes;serializer:gob"`
	}

	dbArchivedContract struct {
		Model
		FCID           types.FileContractID `gorm:"unique;index,type:bytes;serializer:gob;NOT NULL;column:fcid"`
		FileSize       uint64
		Host           consensus.PublicKey  `gorm:"index;type:bytes;serializer:gob;NOT NULL"`
		RenewedTo      types.FileContractID `gorm:"unique;index,type:bytes;serializer:gob"`
		Reason         string
		RevisionNumber uint64
		WindowStart    types.BlockHeight
		WindowEnd      types.BlockHeight
	}

	dbContractSector struct {
		DBContractID uint `gorm:"primaryKey"`
		DBSectorID   uint `gorm:"primaryKey"`
	}

	dbFileContractRevision struct {
		Model
		DBContractID uint `gorm:"unique;index"`

		NewRevisionNumber     uint64 `gorm:"index"`
		NewFileSize           uint64
		NewFileMerkleRoot     crypto.Hash                  `gorm:"type:bytes;serializer:gob"`
		NewWindowStart        types.BlockHeight            `gorm:"index"`
		NewWindowEnd          types.BlockHeight            `gorm:"index"`
		NewValidProofOutputs  []dbValidSiacoinOutput       `gorm:"constraint:OnDelete:CASCADE;NOT NULL"` // CASCADE to delete output
		NewMissedProofOutputs []dbMissedSiacoinOutput      `gorm:"constraint:OnDelete:CASCADE;NOT NULL"` // CASCADE to delete output
		NewUnlockHash         types.UnlockHash             `gorm:"index,type:bytes;serializer:gob"`
		Signatures            []types.TransactionSignature `gorm:"type:bytes;serializer:gob;NOT NULL"`
		UnlockConditions      types.UnlockConditions       `gorm:"NOT NULL;type:bytes;serializer:gob"`
	}

	dbValidSiacoinOutput struct {
		Model
		DBFileContractRevisionID uint `gorm:"index"`

		UnlockHash types.UnlockHash `gorm:"index;type:bytes;serializer:gob"`
		Value      *big.Int         `gorm:"type:bytes;serializer:gob"`
	}

	dbMissedSiacoinOutput struct {
		Model
		DBFileContractRevisionID uint `gorm:"index"`

		UnlockHash types.UnlockHash `gorm:"index;type:bytes;serializer:gob"`
		Value      *big.Int         `gorm:"type:bytes;serializer:gob"`
	}
)

// TableName implements the gorm.Tabler interface.
func (dbArchivedContract) TableName() string { return "archived_contracts" }

// TableName implements the gorm.Tabler interface.
func (dbContractSector) TableName() string { return "contract_sectors" }

// TableName implements the gorm.Tabler interface.
func (dbContract) TableName() string { return "contracts" }

// TableName implements the gorm.Tabler interface.
func (dbFileContractRevision) TableName() string { return "file_contract_revisions" }

// TableName implements the gorm.Tabler interface.
func (dbValidSiacoinOutput) TableName() string { return "siacoin_valid_outputs" }

// TableName implements the gorm.Tabler interface.
func (dbMissedSiacoinOutput) TableName() string { return "siacoin_missed_outputs" }

// TableName implements the gorm.Tabler interface.
func (dbContractSet) TableName() string { return "contract_sets" }

// TableName implements the gorm.Tabler interface.
func (dbContractSetEntry) TableName() string { return "contract_set_entries" }

// convert converts a dbFileContractRevision to a types.FileContractRevision type.
func (r dbFileContractRevision) convert(fcid types.FileContractID) types.FileContractRevision {
	// Prepare valid and missed outputs.
	newValidOutputs := make([]types.SiacoinOutput, len(r.NewValidProofOutputs))
	for i, sco := range r.NewValidProofOutputs {
		newValidOutputs[i] = types.SiacoinOutput{
			Value:      types.NewCurrency(sco.Value),
			UnlockHash: sco.UnlockHash,
		}
	}
	newMissedOutputs := make([]types.SiacoinOutput, len(r.NewMissedProofOutputs))
	for i, sco := range r.NewMissedProofOutputs {
		newMissedOutputs[i] = types.SiacoinOutput{
			Value:      types.NewCurrency(sco.Value),
			UnlockHash: sco.UnlockHash,
		}
	}
	// Prepare pubkeys.
	publickeys := make([]types.SiaPublicKey, len(r.UnlockConditions.PublicKeys))
	for i, pk := range r.UnlockConditions.PublicKeys {
		publickeys[i] = types.SiaPublicKey{
			Algorithm: pk.Algorithm,
			Key:       pk.Key,
		}
	}
	// Prepare revision.
	return types.FileContractRevision{
		ParentID: fcid,
		UnlockConditions: types.UnlockConditions{
			Timelock:           r.UnlockConditions.Timelock,
			PublicKeys:         publickeys,
			SignaturesRequired: r.UnlockConditions.SignaturesRequired,
		},
		NewRevisionNumber:     r.NewRevisionNumber,
		NewFileSize:           r.NewFileSize,
		NewFileMerkleRoot:     r.NewFileMerkleRoot,
		NewWindowStart:        r.NewWindowStart,
		NewWindowEnd:          r.NewWindowEnd,
		NewValidProofOutputs:  newValidOutputs,
		NewMissedProofOutputs: newMissedOutputs,
		NewUnlockHash:         r.NewUnlockHash,
	}
}

// convert converts a dbContractRHPv2 to a rhpv2.Contract type.
func (c dbContract) convert() (bus.Contract, error) {
	// Prepare revision.
	revision := c.Revision.convert(c.FCID)

	// Prepare signatures.
	var signatures [2]types.TransactionSignature
	if len(c.Revision.Signatures) != len(signatures) {
		return bus.Contract{}, fmt.Errorf("contract in db got %v signatures but expected %v", len(c.Revision.Signatures), len(signatures))
	}
	for i, sig := range c.Revision.Signatures {
		signatures[i] = types.TransactionSignature{
			ParentID:       crypto.Hash(sig.ParentID),
			PublicKeyIndex: sig.PublicKeyIndex,
			Timelock:       sig.Timelock,
			CoveredFields:  sig.CoveredFields,
			Signature:      sig.Signature,
		}
	}
	return bus.Contract{
		HostIP:      c.Host.NetAddress(),
		StartHeight: c.StartHeight,
		Revision:    revision,
		Signatures:  signatures,
		ContractMetadata: bus.ContractMetadata{
			RenewedFrom: c.RenewedFrom,
			TotalCost:   types.NewCurrency(c.TotalCost),
			Spending:    bus.ContractSpending{}, // TODO
		},
	}, nil
}

// AcquireContract acquires a contract assuming that the contract exists and
// that it isn't locked right now. The returned bool indicates whether locking
// the contract was successful.
func (s *SQLStore) AcquireContract(fcid types.FileContractID, duration time.Duration) (types.FileContractRevision, bool, error) {
	var contract dbContract
	var locked bool

	fcidGob := bytes.NewBuffer(nil)
	if err := gob.NewEncoder(fcidGob).Encode(fcid); err != nil {
		return types.FileContractRevision{}, false, err
	}

	err := s.db.Transaction(func(tx *gorm.DB) error {
		// Get revision.
		err := tx.Model(&dbContract{}).
			Where("fcid", fcidGob.Bytes()).
			Preload("Revision.NewValidProofOutputs").
			Preload("Revision.NewMissedProofOutputs").
			Take(&contract).
			Error
		if err != nil {
			return err
		}
		// See if it is locked.
		locked = time.Now().Before(contract.LockedUntil)
		if locked {
			return nil
		}

		// Update lock.
		return tx.Model(&dbContract{}).
			Where("fcid", fcidGob.Bytes()).
			Update("locked_until", time.Now().Add(duration).UTC()).
			Error
	})
	if err != nil {
		return types.FileContractRevision{}, false, fmt.Errorf("failed to lock contract: %w", err)
	}
	if locked {
		return types.FileContractRevision{}, false, nil
	}
	return contract.Revision.convert(fcid), true, nil
}

// ReleaseContract releases a contract by setting its locked_until field to 0.
func (s *SQLStore) ReleaseContract(fcid types.FileContractID) error {
	fcidGob := bytes.NewBuffer(nil)
	if err := gob.NewEncoder(fcidGob).Encode(fcid); err != nil {
		return err
	}
	return s.db.Model(&dbContract{}).
		Where("fcid", fcidGob.Bytes()).
		Update("locked_until", time.Time{}).
		Error
}

// addContract implements the bus.ContractStore interface.
func addContract(tx *gorm.DB, c rhpv2.ContractRevision, totalCost types.Currency, startHeight uint64, renewedFrom types.FileContractID) (dbContract, error) {
	fcid := c.ID()

	// Prepare valid and missed outputs.
	newValidOutputs := make([]dbValidSiacoinOutput, len(c.Revision.NewValidProofOutputs))
	for i, sco := range c.Revision.NewValidProofOutputs {
		newValidOutputs[i] = dbValidSiacoinOutput{
			UnlockHash: sco.UnlockHash,
			Value:      sco.Value.Big(),
		}
	}
	newMissedOutputs := make([]dbMissedSiacoinOutput, len(c.Revision.NewMissedProofOutputs))
	for i, sco := range c.Revision.NewMissedProofOutputs {
		newMissedOutputs[i] = dbMissedSiacoinOutput{
			UnlockHash: sco.UnlockHash,
			Value:      sco.Value.Big(),
		}
	}

	// Prepare contract revision.
	revision := dbFileContractRevision{
		Signatures:            c.Signatures[:],
		UnlockConditions:      c.Revision.UnlockConditions,
		NewRevisionNumber:     c.Revision.NewRevisionNumber,
		NewFileSize:           c.Revision.NewFileSize,
		NewFileMerkleRoot:     c.Revision.NewFileMerkleRoot,
		NewWindowStart:        c.Revision.NewWindowStart,
		NewWindowEnd:          c.Revision.NewWindowEnd,
		NewValidProofOutputs:  newValidOutputs,
		NewMissedProofOutputs: newMissedOutputs,
		NewUnlockHash:         c.Revision.NewUnlockHash,
	}

	// Find host.
	var host dbHost
	err := tx.Where(&dbHost{PublicKey: c.HostKey()}).
		Take(&host).Error
	if err != nil {
		return dbContract{}, err
	}

	// Create contract.
	contract := dbContract{
		FCID:        fcid,
		HostID:      host.ID,
		RenewedFrom: renewedFrom,
		Revision:    revision,
		StartHeight: startHeight,
		TotalCost:   totalCost.Big(),
	}

	// Insert contract.
	err = tx.
		Where(&dbHost{PublicKey: c.HostKey()}).
		Create(&contract).Error
	if err != nil {
		return dbContract{}, err
	}
	return contract, nil
}

// AddContract implements the bus.ContractStore interface.
func (s *SQLStore) AddContract(c rhpv2.ContractRevision, totalCost types.Currency, startHeight uint64) (_ bus.Contract, err error) {
	var added dbContract

	if err := s.db.Transaction(func(tx *gorm.DB) error {
		added, err = addContract(tx, c, totalCost, startHeight, types.FileContractID{})
		return err
	}); err != nil {
		return bus.Contract{}, err
	}

	return added.convert()
}

// AddRenewedContract adds a new contract which was created as the result of a renewal to the store.
// The old contract specified as 'renewedFrom' will be deleted from the active
// contracts and moved to the archive. Both new and old contract will be linked
// to each other through the RenewedFrom and RenewedTo fields respectively.
func (s *SQLStore) AddRenewedContract(c rhpv2.ContractRevision, totalCost types.Currency, startHeight uint64, renewedFrom types.FileContractID) (bus.Contract, error) {
	var renewed dbContract

	if err := s.db.Transaction(func(tx *gorm.DB) error {
		// Fetch contract we renew from.
		oldContract, err := contract(tx, renewedFrom)
		if err != nil {
			return err
		}

		// Create copy in archive.
		err = tx.Create(&dbArchivedContract{
			FCID:           oldContract.FCID,
			Host:           oldContract.Host.PublicKey,
			Reason:         archivalReasonRenewed,
			RenewedTo:      c.ID(),
			RevisionNumber: oldContract.Revision.NewRevisionNumber,
			FileSize:       oldContract.Revision.NewFileSize,
			WindowStart:    oldContract.Revision.NewWindowStart,
			WindowEnd:      oldContract.Revision.NewWindowEnd,
		}).Error
		if err != nil {
			return err
		}

		// Delete the contract from the regular table.
		err = removeContract(tx, renewedFrom)
		if err != nil {
			return err
		}

		// Add the new contract.
		renewed, err = addContract(tx, c, totalCost, startHeight, renewedFrom)
		return err
	}); err != nil {
		return bus.Contract{}, err
	}

	return renewed.convert()
}

// Contract implements the bus.ContractStore interface.
func (s *SQLStore) Contract(id types.FileContractID) (bus.Contract, error) {
	// Fetch contract.
	contract, err := s.contract(id)
	if err != nil {
		return bus.Contract{}, err
	}
	return contract.convert()
}

// Contracts implements the bus.ContractStore interface.
func (s *SQLStore) Contracts() ([]bus.Contract, error) {
	dbContracts, err := s.contracts()
	if err != nil {
		return nil, err
	}
	contracts := make([]bus.Contract, len(dbContracts))
	for i, c := range dbContracts {
		contract, err := c.convert()
		if err != nil {
			return nil, err
		}
		contracts[i] = contract
	}
	return contracts, nil
}

// removeContract implements the bus.ContractStore interface.
func removeContract(tx *gorm.DB, id types.FileContractID) error {
	var contract dbContract
	if err := tx.Where(&dbContract{FCID: id}).
		Take(&contract).Error; err != nil {
		return err
	}
	return tx.Where(&dbContract{Model: Model{ID: contract.ID}}).
		Delete(&contract).Error
}

// RemoveContract implements the bus.ContractStore interface.
func (s *SQLStore) RemoveContract(id types.FileContractID) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		return removeContract(tx, id)
	})
}

func contract(tx *gorm.DB, id types.FileContractID) (dbContract, error) {
	var contract dbContract
	err := tx.Where(&dbContract{FCID: id}).
		Preload("Revision.NewValidProofOutputs").
		Preload("Revision.NewMissedProofOutputs").
		Preload("Host").
		Take(&contract).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return contract, ErrContractNotFound
	}
	return contract, err
}

func (s *SQLStore) contract(id types.FileContractID) (dbContract, error) {
	return contract(s.db, id)
}

func (s *SQLStore) contracts() ([]dbContract, error) {
	var contracts []dbContract
	err := s.db.Model(&dbContract{}).
		Preload("Revision.NewValidProofOutputs").
		Preload("Revision.NewMissedProofOutputs").
		Preload("Host.Announcements").
		Find(&contracts).Error
	return contracts, err
}
