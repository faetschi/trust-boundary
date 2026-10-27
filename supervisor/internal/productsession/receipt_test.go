package productsession

import (
	"encoding/json"
	"testing"
)

// unverified_inputs became required in the receipt, so a receipt written before that is v1 and refused.
func TestDecodeReceiptRefusesTheSchemaBeforeRequiredUnverifiedInputs(t *testing.T) {
	for schema, accepted := range map[string]bool{"tbound-product-turn-receipt/v1": false, "tbound-product-turn-receipt/v2": true} {
		data, err := json.Marshal(TurnReceipt{SchemaVersion: schema})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeReceipt(data); (err == nil) != accepted {
			t.Errorf("schema %s: accepted=%v, want %v (%v)", schema, err == nil, accepted, err)
		}
	}
}
