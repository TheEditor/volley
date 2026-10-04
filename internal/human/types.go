// Package human owns exact input records and question generations.
package human

type Input struct {
	Text           string `json:"text"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}
type Question struct {
	ID              string
	Text            []byte
	Hash            string
	ObservationHash string
}
type Receipt struct {
	ID           string
	RunID        string
	Kind         string
	Key          string
	GenerationID *string
	Text         string
	Hash         string
}
type JournalEntry struct {
	RecordVersion int
	Sequence      uint64
	PreviousHash  string
	RunID         string
	Kind          string
	Key           string
	GenerationID  *string
	ReceiptPath   string
	ReceiptHash   string
}
