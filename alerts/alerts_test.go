package alerts

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"go.sia.tech/core/types"
	"go.sia.tech/renterd/webhooks"
	"go.uber.org/zap"
)

func TestWebhooks(t *testing.T) {
	mgr := webhooks.NewManager(zap.NewNop().Sugar())
	alerts := NewManager(mgr)

	mux := http.NewServeMux()
	var events []webhooks.Action
	var mu sync.Mutex
	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		var event webhooks.Action
		if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// register a hook
	wh := webhooks.Webhook{
		Module: webhookModule,
		URL:    fmt.Sprintf("http://%v/events", srv.Listener.Addr().String()),
	}
	if hookID := wh.String(); hookID != fmt.Sprintf("%v.%v.%v", wh.URL, wh.Module, "") {
		t.Fatalf("wrong result for wh.String(): %v != %v", wh.String(), hookID)
	}
	err := mgr.Register(wh)
	if err != nil {
		t.Fatal(err)
	}

	// perform some actions that should trigger the endpoint
	a := Alert{
		ID:        types.Hash256{1},
		Message:   "test",
		Severity:  SeverityWarning,
		Timestamp: time.Unix(0, 0),
		Data: map[string]interface{}{
			"origin": "foo",
		},
	}
	if err := alerts.RegisterAlert(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if err := alerts.DismissAlerts(context.Background(), types.Hash256{1}); err != nil {
		t.Fatal(err)
	}

	// list hooks
	hooks, _ := mgr.Info()
	if len(hooks) != 1 {
		t.Fatal("wrong number of hooks")
	} else if hooks[0].URL != wh.URL {
		t.Fatal("wrong hook id")
	} else if hooks[0].Event != wh.Event {
		t.Fatal("wrong event", hooks[0].Event)
	} else if hooks[0].Module != wh.Module {
		t.Fatal("wrong module", hooks[0].Module)
	}

	// unregister hook
	if !mgr.Delete(webhooks.Webhook{
		Event:  hooks[0].Event,
		Module: hooks[0].Module,
		URL:    hooks[0].URL,
	}) {
		t.Fatal("hook not deleted")
	}

	// perform an action that should not trigger the endpoint
	if err := alerts.RegisterAlert(context.Background(), Alert{
		ID:        types.Hash256{2},
		Message:   "test",
		Severity:  SeverityWarning,
		Timestamp: time.Now(),
		Data: map[string]interface{}{
			"origin": "foo",
		},
	}); err != nil {
		t.Fatal(err)
	}

	// check events
	for i := 0; i < 10; i++ {
		mu.Lock()
		nEvents := len(events)
		mu.Unlock()
		if nEvents != 3 {
			time.Sleep(100 * time.Millisecond)
			continue
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 3 {
		t.Fatal("wrong number of events", len(events))
	}
	assertEvent := func(event webhooks.Action, module, id string, hasPayload bool) {
		t.Helper()
		if event.Module != module {
			t.Fatal("wrong event module", event.Module, module)
		} else if event.Event != id {
			t.Fatal("wrong event id", event.Event, id)
		} else if hasPayload && event.Payload == nil {
			t.Fatal("missing payload")
		}
	}
	assertEvent(events[0], "", webhooks.WebhookEventPing, false)
	assertEvent(events[1], webhookModule, webhookEventRegister, true)
	assertEvent(events[2], webhookModule, webhookEventDismiss, true)
}
