package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestEnsureFolderUpdatesGranularConfig(t *testing.T) {
	var method, path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		if r.Method == http.MethodGet {
			http.NotFound(w, r)
			return
		}
		var folder Folder
		if err := json.NewDecoder(r.Body).Decode(&folder); err != nil {
			t.Errorf("decode folder: %v", err)
		}
		if folder.ID != "folder" || !reflect.DeepEqual(folder.Devices, []FolderDevice{{DeviceID: "peer"}}) {
			t.Errorf("unexpected folder request: %+v", folder)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client := &Syncthing{BaseURL: server.URL}
	err := client.EnsureFolder(context.Background(), Folder{ID: "folder", Path: "/data/folder", Type: "sendreceive", Devices: []FolderDevice{{DeviceID: "peer"}}})
	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPost || path != "/rest/config/folders" {
		t.Fatalf("request = %s %s, want POST /rest/config/folders", method, path)
	}
}

func TestParseCompletionClamps(t *testing.T) {
	for _, test := range []struct {
		input float64
		want  int32
	}{{-1, 0}, {37.9, 37}, {101, 100}} {
		if got := ParseCompletion(Completion{Completion: test.input}); got != test.want {
			t.Errorf("ParseCompletion(%v) = %d, want %d", test.input, got, test.want)
		}
	}
}

func TestDiscoveryConfigIsAppliedWithoutDroppingUnknownValues(t *testing.T) {
	var updated map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"options":{"localAnnounceEnabled":true,"globalAnnounceEnabled":true,"relaysEnabled":true,"natEnabled":true,"startBrowser":true}}`))
		case http.MethodPut:
			if err := json.NewDecoder(r.Body).Decode(&updated); err != nil {
				t.Errorf("decode config update: %v", err)
			}
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}))
	defer server.Close()
	client := &Syncthing{BaseURL: server.URL}
	agent := &Agent{Syncthing: client}
	if err := agent.reconcileSyncthing(context.Background()); err != nil {
		t.Fatal(err)
	}
	options := updated["options"].(map[string]any)
	for _, key := range []string{"localAnnounceEnabled", "globalAnnounceEnabled", "relaysEnabled", "natEnabled"} {
		if options[key] != false {
			t.Errorf("%s = %v, want disabled", key, options[key])
		}
	}
	if options["startBrowser"] != true {
		t.Errorf("unrelated configuration changed: startBrowser=%v", options["startBrowser"])
	}
}
