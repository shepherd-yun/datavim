package db

// Collect reads a whole result through a stream function.
func Collect(stream func(onColumns func([]ColumnInfo), onRow func([]Value) error) error) (*ResultSet, error) {
	rs := &ResultSet{}
	err := stream(
		func(cols []ColumnInfo) { rs.Columns = cols },
		func(row []Value) error {
			rs.Rows = append(rs.Rows, row)
			return nil
		})
	if err != nil {
		return nil, err
	}
	return rs, nil
}
