package export

import (
	"errors"
	"fmt"

	"github.com/xuri/excelize/v2"
)

// streamSheetColumn は共通のデータシート骨格に必要な列情報だけを持つ。
// 値の生成は各帳票の責務なので含めない。
type streamSheetColumn struct {
	header string
	width  float64
}

// streamDataSheet は一覧系xlsxで共通するStreamWriterのライフサイクルを管理する。
// New→行出力→Finishの順で使い、帳票固有の行変換や入力規則は呼び出し側に残す。
type streamDataSheet struct {
	file     *excelize.File
	writer   *excelize.StreamWriter
	sheet    string
	colCount int
}

// newStreamDataSheet は列幅、固定ヘッダ、共通style、ヘッダ行を設定する。
// StreamWriterでは最初のSetRowより前に列幅とpaneを設定する必要があるため、
// この順序を共通化して帳票間の差異を防ぐ。
func newStreamDataSheet(f *excelize.File, sheet string, cols []streamSheetColumn) (*streamDataSheet, error) {
	if f == nil {
		return nil, errors.New("Excelファイルがnilです")
	}
	if len(cols) == 0 {
		return nil, errors.New("出力列がありません")
	}

	sw, err := f.NewStreamWriter(sheet)
	if err != nil {
		return nil, err
	}
	for i, c := range cols {
		if err := sw.SetColWidth(i+1, i+1, c.width); err != nil {
			return nil, err
		}
	}
	if err := sw.SetPanes(&excelize.Panes{
		Freeze:      true,
		YSplit:      1,
		TopLeftCell: "A2",
		ActivePane:  "bottomLeft",
		Selection:   []excelize.Selection{{Pane: "bottomLeft", ActiveCell: "A2", SQRef: "A2"}},
	}); err != nil {
		return nil, err
	}
	headerStyle, err := f.NewStyle(&excelize.Style{
		Font: &excelize.Font{Bold: true},
		Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"#DDEBF7"}},
	})
	if err != nil {
		return nil, err
	}
	header := make([]any, 0, len(cols))
	for _, c := range cols {
		header = append(header, excelize.Cell{StyleID: headerStyle, Value: c.header})
	}
	if err := sw.SetRow("A1", header); err != nil {
		return nil, err
	}
	return &streamDataSheet{file: f, writer: sw, sheet: sheet, colCount: len(cols)}, nil
}

// Finish はヘッダへAutoFilterを設定し、帳票固有のFlush前処理を実行してから
// StreamWriterを確定する。入力規則のようにFlush前でなければならない処理は
// beforeFlushへ渡す。
func (s *streamDataSheet) Finish(beforeFlush func() error) error {
	lastCol, err := excelize.ColumnNumberToName(s.colCount)
	if err != nil {
		return err
	}
	if err := s.file.AutoFilter(s.sheet, fmt.Sprintf("A1:%s1", lastCol), nil); err != nil {
		return err
	}
	if beforeFlush != nil {
		if err := beforeFlush(); err != nil {
			return err
		}
	}
	return s.writer.Flush()
}
