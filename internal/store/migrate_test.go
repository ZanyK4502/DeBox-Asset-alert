package store

import "testing"

func TestMigrationVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		want    int64
		wantErr bool
	}{
		{name: "0001_python_baseline.sql", want: 1},
		{name: "0012_add_index.sql", want: 12},
		{name: "missing.sql", wantErr: true},
		{name: "zero_invalid.sql", wantErr: true},
		{name: "x_invalid.sql", wantErr: true},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := migrationVersion(test.name)
			if test.wantErr {
				if err == nil {
					t.Fatal("migrationVersion() expected an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("migrationVersion(): %v", err)
			}
			if got != test.want {
				t.Fatalf("migrationVersion() = %d, want %d", got, test.want)
			}
		})
	}
}
