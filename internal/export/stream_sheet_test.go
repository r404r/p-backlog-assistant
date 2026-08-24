package export

import (
	"testing"

	"github.com/xuri/excelize/v2"
)

func TestNewStreamDataSheetRejectsEmptyColumns(t *testing.T) {
	f := excelize.NewFile()
	defer f.Close()
	if _, err := newStreamDataSheet(f, f.GetSheetName(0), nil); err == nil {
		t.Fatal("列なしでエラーにならなかった")
	}
}
