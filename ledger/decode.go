package ledger

import (
	"bytes"
	"encoding/json"
)

// decodeKeepingNumbers decodes one JSON value into v, keeping every number a
// json.Number. A plain json.Unmarshal into any turns a number into a float64,
// so 2^53+1 comes back as 2^53, and a number outside float64's range cannot be
// decoded at all — which makes the entry holding it, and every later entry of
// its stream, unreadable. Every reader that hands a stored JSON value back to
// a caller decodes through here, so the number is the one that was stored.
func decodeKeepingNumbers(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	return dec.Decode(v)
}
