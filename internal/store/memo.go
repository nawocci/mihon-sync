package store

import (
	"bytes"
	"encoding/json"
)

// mergeMemo combines two memo blobs. Memo is arbitrary JSON written by the app,
// so the merge is a shallow key-wise union with the incoming side winning on a
// key conflict. Anything that is not a JSON object on both sides falls back to
// preferring the incoming value when it is present.
func mergeMemo(existing, incoming []byte) []byte {
	if len(incoming) == 0 {
		return existing
	}
	if len(existing) == 0 {
		return incoming
	}

	var exObj, inObj map[string]json.RawMessage
	if json.Unmarshal(existing, &exObj) != nil || json.Unmarshal(incoming, &inObj) != nil {
		return incoming
	}
	if exObj == nil {
		exObj = map[string]json.RawMessage{}
	}
	for k, v := range inObj {
		exObj[k] = v
	}
	merged, err := json.Marshal(exObj)
	if err != nil {
		return incoming
	}
	return merged
}

// sameBlob reports whether two memo blobs are byte-identical.
func sameBlob(a, b []byte) bool {
	return bytes.Equal(a, b)
}
