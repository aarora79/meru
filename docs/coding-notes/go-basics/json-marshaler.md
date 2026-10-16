# JSON marshalers

**In one line:** a type with a `MarshalJSON() ([]byte, error)` method decides
for itself what JSON `encoding/json` writes for it.

## Why Go has it

`encoding/json` writes a struct as an object, one key per exported field, and
struct tags rename or drop keys. That covers most types. Some data has to come
out in more than one shape, such as a value that is sometimes a string and
sometimes an object. A type that has a `MarshalJSON` method takes over: the
encoder calls it and writes whatever bytes it returns. The method is how a type
satisfies the `json.Marshaler` interface, with no "implements" keyword.

## Smallest example

```go
type item struct {
	Text  string
	Image []byte
}

// MarshalJSON writes text alone as a string, and anything with an image as
// an object. []byte comes out as base64.
func (it item) MarshalJSON() ([]byte, error) {
	if it.Image == nil {
		return json.Marshal(it.Text)
	}
	return json.Marshal(struct {
		Text  string `json:"text,omitempty"`
		Image []byte `json:"image"`
	}{it.Text, it.Image})
}

b, _ := json.Marshal([]item{{Text: "a"}, {Image: []byte("hi")}})
// ["a",{"image":"aGk="}]
```

## Where Meru uses it

- `internal/engine/ollama_wire.go` — `embedItem` is a plain string for text and
  an object for an image, because Ollama's `/api/embed` takes both in one list.

## Mistakes to avoid

- Don't call `json.Marshal(it)` inside `MarshalJSON` on the same type: it calls
  the method again and never stops. Marshal another value, such as the field or
  an anonymous struct, as above.
- Give the method a value receiver, `(it item)`, so both `item` and `*item`
  use it. A pointer receiver is skipped when the encoder holds a plain value.
