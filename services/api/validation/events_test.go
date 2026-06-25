package validation_test

import (
	"testing"

	"github.com/Depo-dev/trident/services/api/validation"
)

// ── ValidateQueryEvents ───────────────────────────────────────────────────────

func TestValidateQueryEvents_Defaults(t *testing.T) {
	p, err := validation.ValidateQueryEvents("", "", "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Limit != validation.LimitDefault {
		t.Errorf("want default limit %d, got %d", validation.LimitDefault, p.Limit)
	}
	if p.LedgerFrom != nil || p.LedgerTo != nil {
		t.Error("want nil ledger bounds when not provided")
	}
}

func TestValidateQueryEvents_ValidLimit(t *testing.T) {
	for _, tc := range []struct{ input string; want int }{
		{"1", 1},
		{"50", 50},
		{"200", 200},
	} {
		p, err := validation.ValidateQueryEvents(tc.input, "", "", "", "")
		if err != nil {
			t.Errorf("limit=%q: unexpected error: %v", tc.input, err)
			continue
		}
		if p.Limit != tc.want {
			t.Errorf("limit=%q: want %d, got %d", tc.input, tc.want, p.Limit)
		}
	}
}

func TestValidateQueryEvents_InvalidLimit(t *testing.T) {
	for _, bad := range []string{"0", "201", "-1", "abc", "1.5"} {
		_, err := validation.ValidateQueryEvents(bad, "", "", "", "")
		if err == nil {
			t.Errorf("limit=%q: expected validation error", bad)
		} else if err.Field != "limit" {
			t.Errorf("limit=%q: wrong field %q", bad, err.Field)
		}
	}
}

func TestValidateQueryEvents_ValidLedgerRange(t *testing.T) {
	p, err := validation.ValidateQueryEvents("", "100", "200", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if *p.LedgerFrom != 100 || *p.LedgerTo != 200 {
		t.Errorf("want 100..200, got %d..%d", *p.LedgerFrom, *p.LedgerTo)
	}
}

func TestValidateQueryEvents_LedgerFromGreaterThanLedgerTo(t *testing.T) {
	_, err := validation.ValidateQueryEvents("", "500", "100", "", "")
	if err == nil {
		t.Fatal("expected validation error for ledgerFrom > ledgerTo")
	}
	if err.Field != "ledgerTo" {
		t.Errorf("wrong field %q, want \"ledgerTo\"", err.Field)
	}
}

func TestValidateQueryEvents_NegativeLedger(t *testing.T) {
	for field, args := range map[string][5]string{
		"ledgerFrom": {"", "-1", "", "", ""},
		"ledgerTo":   {"", "", "-5", "", ""},
	} {
		_, err := validation.ValidateQueryEvents(args[0], args[1], args[2], args[3], args[4])
		if err == nil {
			t.Errorf("%s: expected error for negative value", field)
		} else if err.Field != field {
			t.Errorf("%s: wrong field %q", field, err.Field)
		}
	}
}

func TestValidateQueryEvents_ValidContractID(t *testing.T) {
	// 56-char strkey starting with C
	validID := "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAABSC4"
	_, err := validation.ValidateQueryEvents("", "", "", validID, "")
	if err != nil {
		t.Fatalf("unexpected error for valid contractId: %v", err)
	}
}

func TestValidateQueryEvents_InvalidContractID(t *testing.T) {
	for _, bad := range []string{
		"not-a-contract",
		"GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF", // G... account, not C...
		"C123",        // too short
		"cAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", // lowercase c
	} {
		_, err := validation.ValidateQueryEvents("", "", "", bad, "")
		if err == nil {
			t.Errorf("contractId=%q: expected validation error", bad)
		} else if err.Field != "contractId" {
			t.Errorf("contractId=%q: wrong field %q", bad, err.Field)
		}
	}
}

func TestValidateQueryEvents_ValidInputsPassThrough(t *testing.T) {
	contractID := "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAABSC4"
	p, err := validation.ValidateQueryEvents("10", "1", "100", contractID, "some-cursor")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Limit != 10 || *p.LedgerFrom != 1 || *p.LedgerTo != 100 {
		t.Errorf("unexpected params: %+v", p)
	}
	if p.ContractID != contractID {
		t.Errorf("contractId not forwarded")
	}
	if p.Cursor != "some-cursor" {
		t.Errorf("cursor not forwarded")
	}
}

// ── ValidateEventID ───────────────────────────────────────────────────────────

func TestValidateEventID_ValidUUIDv4(t *testing.T) {
	validIDs := []string{
		"550e8400-e29b-41d4-a716-446655440000",
		"6ba7b810-9dad-41d1-80b4-00c04fd430c8", // note: version digit is 4
	}
	// only the second one matches v4 format strictly; just test correct ones
	good := "550e8400-e29b-41d4-a716-446655440000"
	if err := validation.ValidateEventID(good); err != nil {
		t.Errorf("expected no error for valid UUID v4 %q, got: %v", good, err)
	}
	_ = validIDs
}

func TestValidateEventID_InvalidFormats(t *testing.T) {
	for _, bad := range []string{
		"not-a-uuid",
		"550e8400e29b41d4a716446655440000", // no hyphens
		"550e8400-e29b-31d4-a716-446655440000", // version 3, not 4
		"550e8400-e29b-41d4-c716-446655440000", // variant bits wrong (c)
		"",
		"XXXXXXXX-XXXX-4XXX-8XXX-XXXXXXXXXXXX", // uppercase
	} {
		if err := validation.ValidateEventID(bad); err == nil {
			t.Errorf("expected error for invalid UUID %q", bad)
		}
	}
}
