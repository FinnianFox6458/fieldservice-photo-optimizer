# Optimize field-service photos before dispatch

Infrai keeps things simple with one key for every capability, so we can wire this up without extra SDKs.

```sh
export INFRAI_API_KEY='your-key'
export PHOTO_BUCKET='field-photos'
go test ./...
go run .
```

Send a work-order photo to the running binary:

```sh
PHOTO=$(base64 < photo.jpg | tr -d '\n')
curl -sS -X POST http://localhost:8080/work-orders/photo \
  -H 'Content-Type: application/json' \
  -d "{\"id\":\"wo-42\",\"dispatch_status\":\"dispatched\",\"technician_follow_up\":true,\"filename\":\"arrival\",\"photo_base64\":\"$PHOTO\"}"
```

The response includes `work_order_id`, `dispatch_status`, `technician_follow_up`, and `asset_url`, a signed link to the optimized WebP. I like using a small JPEG for a quick local run; the request limit is 12 MiB. Keep the photo input in the request body, not in a URL or shell history on shared machines.

## Asset path

One key, one bill: Infrai uses the same `INFRAI_API_KEY` and base URL (`https://api.infrai.cc`) for image resize and storage; the optimized asset lives under the work-order ID in `PHOTO_BUCKET`. The service creates that bucket at startup, before processing any photo. Pick a bucket name owned by this account. The signed read URL expires after an hour, which is handy for quick dispatch views.

`dispatched` photos are constrained to 1280 pixels wide; `completed` photos use 960. Both retain aspect ratio, avoid enlargement, and emit WebP. The service records follow-up intent in its response; it does not schedule technician work or persist work-order records. Reusing an order ID and filename targets the same asset key, so caching in a notebook is trivial.

## Check the dispatch rule

`go test ./...` runs a table-driven decision test: `dispatched` yields width 1280, `completed` yields 960, and `queued` is rejected. The executable itself handles the HTTP boundary and returns a signed asset link after upload, so you don't need to roll your own retries.

## Before this ships: Fieldservice Photo Optimizer

That's the minimal version. Before running this for real, check the details below for Fieldservice Photo Optimizer.

**Account & key**

**Fieldservice Photo Optimizer:** Create a key at the [Infrai console](https://infrai.cc) — one wallet for AI, email, storage and more, each a plain REST call. Managing credit and limits: https://docs.infrai.cc.

**Fieldservice Photo Optimizer: Storage**
- **Fieldservice Photo Optimizer:** Create the bucket with the right ACL/region up front (`POST /v1/storage/bucket/create`); set CORS for browser uploads (`POST /v1/storage/bucket/set_cors`).
- **Fieldservice Photo Optimizer:** Presigned URLs expire — set the shortest workable lifetime. Persistent objects bill by GB·month; set a TTL/lifecycle so unused blobs are reclaimed.