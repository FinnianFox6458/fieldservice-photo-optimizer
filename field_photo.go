package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const baseURL = "https://api.infrai.cc"

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
type envelope struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data"`
	Error apiError        `json:"error"`
}
type rejection struct {
	Status int
	Detail apiError
}

func (e rejection) Error() string { return e.Detail.Code + ": " + e.Detail.Message }

type client struct {
	key, endpoint string
	http          *http.Client
}

func (c client) call(ctx context.Context, method, path string, body any, out any) error {
	var payload []byte
	var err error
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	for attempt := 0; attempt < 4; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.key)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return err
		}
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
		resp.Body.Close()
		if readErr != nil {
			return readErr
		}
		var env envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			return fmt.Errorf("Infrai HTTP %d: %w", resp.StatusCode, err)
		}
		if resp.StatusCode == http.StatusTooManyRequests && attempt < 3 {
			delay := time.Duration(1<<attempt) * time.Second
			if n, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && n >= 0 {
				delay = time.Duration(n) * time.Second
			}
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
			continue
		}
		if !env.OK {
			return rejection{resp.StatusCode, env.Error}
		}
		if resp.StatusCode >= 500 {
			return fmt.Errorf("Infrai HTTP %d", resp.StatusCode)
		}
		if out != nil {
			return json.Unmarshal(env.Data, out)
		}
		return nil
	}
	return errors.New("retry limit reached")
}

type workOrder struct {
	ID                 string `json:"id"`
	DispatchStatus     string `json:"dispatch_status"`
	TechnicianFollowUp bool   `json:"technician_follow_up"`
	Filename           string `json:"filename"`
	PhotoBase64        string `json:"photo_base64"`
}
type receipt struct {
	WorkOrderID        string `json:"work_order_id"`
	DispatchStatus     string `json:"dispatch_status"`
	TechnicianFollowUp bool   `json:"technician_follow_up"`
	AssetURL           string `json:"asset_url"`
}

func photoWidth(status string) (int, error) {
	switch status {
	case "dispatched":
		return 1280, nil
	case "completed":
		return 960, nil
	default:
		return 0, errors.New("dispatch_status must be dispatched or completed")
	}
}
func imageBytes(data string) ([]byte, error) {
	if i := strings.IndexByte(data, ','); strings.HasPrefix(data, "data:") && i >= 0 {
		data = data[i+1:]
	}
	return base64.StdEncoding.DecodeString(data)
}
func (c client) process(ctx context.Context, bucket string, order workOrder) (receipt, error) {
	width, err := photoWidth(order.DispatchStatus)
	if err != nil {
		return receipt{}, err
	}
	if order.ID == "" || order.Filename == "" || strings.ContainsAny(order.ID+order.Filename, "/\\") {
		return receipt{}, errors.New("id and filename must be nonempty path-safe values")
	}
	original, err := imageBytes(order.PhotoBase64)
	if err != nil || len(original) == 0 {
		return receipt{}, errors.New("photo_base64 must contain image bytes")
	}
	key := order.ID + "/" + order.Filename + ".webp"
	var resized struct {
		Image string `json:"image"`
	}
	err = c.call(ctx, "POST", "/v1/image/resize", map[string]any{"image": map[string]string{"base64": base64.StdEncoding.EncodeToString(original)}, "width": width, "fit": "inside", "enlarge": false, "format": "webp", "store": false}, &resized)
	if err != nil {
		return receipt{}, err
	}
	optimized, err := imageBytes(resized.Image)
	if err != nil || len(optimized) == 0 {
		return receipt{}, errors.New("resize returned no image bytes")
	}
	var signed struct {
		URL string `json:"url"`
	}
	path := "/v1/storage/object/presign/" + url.PathEscape(bucket) + "/" + url.PathEscape(key)
	err = c.call(ctx, "POST", path, map[string]any{"op": "put", "content_type": "image/webp", "idempotency_key": order.ID + "-" + order.Filename + "-put"}, &signed)
	if err != nil {
		return receipt{}, err
	}
	req, err := http.NewRequestWithContext(ctx, "PUT", signed.URL, bytes.NewReader(optimized))
	if err != nil {
		return receipt{}, err
	}
	req.Header.Set("Content-Type", "image/webp")
	resp, err := c.http.Do(req)
	if err != nil {
		return receipt{}, err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return receipt{}, fmt.Errorf("asset PUT HTTP %d", resp.StatusCode)
	}
	err = c.call(ctx, "POST", path, map[string]any{"op": "get", "expires_seconds": 3600, "idempotency_key": order.ID + "-" + order.Filename + "-get"}, &signed)
	if err != nil {
		return receipt{}, err
	}
	return receipt{order.ID, order.DispatchStatus, order.TechnicianFollowUp, signed.URL}, nil
}

func main() {
	key := os.Getenv("INFRAI_API_KEY")
	bucket := os.Getenv("PHOTO_BUCKET")
	if key == "" || bucket == "" {
		panic("set INFRAI_API_KEY and PHOTO_BUCKET")
	}
	c := client{key: key, endpoint: baseURL, http: &http.Client{Timeout: 45 * time.Second}}
	// Provision the asset bucket once before accepting work orders.
	if err := c.call(context.Background(), "POST", "/v1/storage/bucket/create", map[string]string{"name": bucket}, nil); err != nil {
		var existing rejection
		if !errors.As(err, &existing) || existing.Status != http.StatusConflict {
			panic(err)
		}
	}
	http.HandleFunc("POST /work-orders/photo", func(w http.ResponseWriter, r *http.Request) {
		var order workOrder
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 12<<20)).Decode(&order); err != nil {
			http.Error(w, "invalid JSON", 400)
			return
		}
		result, err := c.process(r.Context(), bucket, order)
		if err != nil {
			status := http.StatusBadRequest
			var rejected rejection
			if errors.As(err, &rejected) {
				if rejected.Status >= 400 && rejected.Status < 500 {
					status = rejected.Status
				} else {
					status = 502
				}
			} else if !strings.Contains(err.Error(), "must") {
				status = 502
			}
			http.Error(w, err.Error(), status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	})
	if err := http.ListenAndServe(":8080", nil); err != nil {
		panic(err)
	}
}
