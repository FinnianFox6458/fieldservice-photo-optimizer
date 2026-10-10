package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPhotoWidth(t *testing.T) {
	cases := []struct {
		status string
		width  int
		valid  bool
	}{
		{"dispatched", 1280, true}, {"completed", 960, true}, {"queued", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			width, err := photoWidth(tc.status)
			if width != tc.width || (err == nil) != tc.valid {
				t.Fatalf("width=%d err=%v", width, err)
			}
		})
	}
}

func TestProcessSendsStructuredImage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/image/resize" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		var body struct {
			Image map[string]string `json:"image"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Image["base64"] != "aW1hZ2U=" {
			t.Fatalf("image = %#v", body.Image)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":false,"error":{"code":"stop","message":"captured"}}`))
	}))
	defer server.Close()

	c := client{endpoint: server.URL, http: server.Client()}
	_, err := c.process(context.Background(), "unused", workOrder{
		ID: "wo-test", DispatchStatus: "dispatched", Filename: "photo", PhotoBase64: "aW1hZ2U=",
	})
	if err == nil || err.Error() != "stop: captured" {
		t.Fatalf("process error = %v", err)
	}
}
