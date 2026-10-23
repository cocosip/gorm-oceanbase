package main

import "testing"

func TestGeneratedKeysMustMatchPersistedRows(t *testing.T) {
	const second = "second"
	want := []generatedRow{{ID: 1, Name: firstName}, {ID: 2, Name: second}}
	for _, got := range [][]generatedRow{
		{{ID: 2, Name: firstName}, {ID: 1, Name: second}},
		{{ID: 3, Name: firstName}, {ID: 4, Name: second}},
		{{ID: 1, Name: firstName}},
	} {
		if validateGeneratedRows(want, got) == nil {
			t.Fatal("incorrect generated keys accepted")
		}
	}
	if err := validateGeneratedRows(want, []generatedRow{want[1], want[0]}); err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{nil, uint(2), int64(-1), float64(1)} {
		if equalIntegerKey(value, 1) {
			t.Fatalf("incorrect map key %v accepted", value)
		}
	}
	for _, value := range []any{uint(1), int64(1)} {
		if !equalIntegerKey(value, 1) {
			t.Fatalf("correct map key %v rejected", value)
		}
	}
}

func TestMigrationRequiresWidenedColumn(t *testing.T) {
	old, widened := int64(32), int64(64)
	if requireWidenedName([]columnInfo{{Name: "NAME", Length: &old}}) == nil {
		t.Fatal("no-op widening accepted")
	}
	if requireWidenedName(nil) == nil {
		t.Fatal("missing column accepted")
	}
	if err := requireWidenedName([]columnInfo{{Name: "NAME", Length: &widened}}); err != nil {
		t.Fatal(err)
	}
}
