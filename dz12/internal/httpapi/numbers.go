package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"marketplace/internal/fault"
)

func checkNumberEncoding(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var value func(string, int) error
	value = func(path string, depth int) error {
		if depth > 64 {
			return fault.Validation("body", "Вложенность JSON не должна превышать 64 уровня")
		}
		if len(path) > 256 {
			return fault.Validation("body", "Путь поля превышает 256 байт")
		}
		token, err := d.Token()
		if err != nil {
			return err
		}
		switch token := token.(type) {
		case json.Number:
			text := string(token)
			bad := len(text) > 64
			if i := strings.IndexAny(text, "eE"); i >= 0 {
				exponent, err := strconv.Atoi(text[i+1:])
				bad = bad || err != nil || exponent < -18 || exponent > 18
			}
			if bad {
				if path == "" {
					path = "body"
				}
				return fault.Validation(path, "Запись числа: не более 64 символов, показатель степени от -18 до 18")
			}
		case json.Delim:
			switch token {
			case '{':
				for d.More() {
					key, err := d.Token()
					if err != nil {
						return err
					}
					name, ok := key.(string)
					if !ok {
						return fmt.Errorf("invalid object key")
					}
					if len(path)+len(name)+1 > 256 {
						return fault.Validation("body", "Путь поля превышает 256 байт")
					}
					if path != "" {
						name = path + "." + name
					}
					if err := value(name, depth+1); err != nil {
						return err
					}
				}
				_, err = d.Token()
				return err
			case '[':
				for i := 0; d.More(); i++ {
					if err := value(fmt.Sprintf("%s[%d]", path, i), depth+1); err != nil {
						return err
					}
				}
				_, err = d.Token()
				return err
			}
		}
		return nil
	}
	err := value("", 0)
	if f, ok := err.(*fault.Error); ok {
		return f
	}
	return nil
}
