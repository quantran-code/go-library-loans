# go-library-loans

Records which member borrowed which book, and when it is due back.

```sh
PORT=8082 go run .
```

| Route | Behaviour |
|---|---|
| `GET /health` | `{"status":"ok"}` when the process is up |
| `POST /loans` | records a borrow and answers `201` with the loan; the catalogue copy goes off the shelf |
| `GET /loans` | every loan, newest first, `[]` when there are none |
| `GET /loans/{id}` | one loan, `404 {"error":"loan not found"}` when unknown, `400 {"error":"invalid loan id"}` for a bad id |
| `POST /loans/{id}/return` | puts the copy back on the shelf and answers `200` with the loan carrying `returnedAt`; `409 {"error":"loan already returned"}`, `409 {"error":"book not on loan"}`, `404 {"error":"loan not found"}`, `400 {"error":"invalid loan id"}`, `502 {"error":"catalog unavailable"}` when the catalogue cannot be updated |

Run the tests with `go test ./...`.
