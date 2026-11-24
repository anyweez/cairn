package codec_test

import (
	"errors"
	"testing"
	"time"

	"github.com/anyweez/cairn/codec"
)

// zzqaTimeKeyMap keys a map by a time.Time. encoding/json writes such a key
// through time.Time's encoding.TextMarshaler, whose RFC 3339 offset is whole
// minutes, so a location whose offset has a seconds part loses it and the
// decoded key is a different instant.
type zzqaTimeKeyMap struct {
	M map[time.Time]int `json:"m"`
}

func (zzqaTimeKeyMap) area() int { return 0 }

// A map keyed by a time the JSON codec cannot carry must not be stored as a
// different instant: Encode either refuses it as not storable, like a
// time.Time held in a field, or the decoded key is the instant the caller
// sent.
func TestATimeMapKeyIsNotStoredAsADifferentInstant(t *testing.T) {
	at := time.Date(1900, time.January, 1, 12, 0, 0, 0, time.FixedZone("AMT", 19*60+32))
	if survivesJSON(at) {
		t.Fatalf("encoding/json carries %v unchanged; nothing to refuse", at)
	}

	c := codec.New[shape]()
	codec.Register[zzqaTimeKeyMap](c, "zzqaTimeKeyMap", 1)

	env, err := c.Encode(zzqaTimeKeyMap{M: map[time.Time]int{at: 1}})
	if err != nil {
		var notStorable *codec.NotStorableError
		if !errors.As(err, &notStorable) {
			t.Fatalf("Encode refused with %v, want a NotStorableError", err)
		}
		return
	}
	decoded, err := c.Decode(env)
	if err != nil {
		t.Fatalf("decoding: %v", err)
	}
	back := decoded.(zzqaTimeKeyMap)
	if len(back.M) != 1 {
		t.Fatalf("stored %d keys, want 1", len(back.M))
	}
	for key := range back.M {
		if !key.Equal(at) {
			t.Fatalf("stored the map key as %s (%s), %s after the instant sent %s (%s)",
				key, key.Location(), key.Sub(at), at, at.Location())
		}
	}
}
