package importer

import "errors"

type xlsxReader struct{ rowReader }

func openXLSX(string, string) (*xlsxReader, error) {
	return nil, errors.New("xlsx is not supported yet")
}
