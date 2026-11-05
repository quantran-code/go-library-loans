# go-library-loans

Records which member borrowed which book, and when it is due back.

```sh
PORT=8082 go run .
```

| Route | Behaviour |
|---|---|
| `GET /health` | `{"status":"ok"}` when the process is up |

Run the tests with `go test ./...`.
