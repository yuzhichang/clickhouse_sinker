/*Copyright [2019] housepower

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

   http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
package parser

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/housepower/clickhouse_sinker/model"
	"github.com/housepower/clickhouse_sinker/util"
	"github.com/pkg/errors"
	"github.com/tidwall/gjson"
	"github.com/valyala/fastjson/fastfloat"
)

var _ Parser = (*CsvParser)(nil)

// CsvParser implementation to parse input from a CSV format per RFC 4180
type CsvParser struct {
	pp *Pool
}

// Parse extract a list of comma-separated values from the data
func (p *CsvParser) Parse(bs []byte) (metric model.Metric, err error) {
	r := csv.NewReader(bytes.NewReader(bs))
	r.FieldsPerRecord = len(p.pp.csvFormat)
	if len(p.pp.delimiter) > 0 {
		r.Comma = rune(p.pp.delimiter[0])
	}
	var value []string
	if value, err = r.Read(); err != nil {
		err = errors.Wrap(err, "")
		return
	}
	if len(value) != len(p.pp.csvFormat) {
		err = errors.Errorf("csv value doesn't match the format")
		return
	}
	metric = &CsvMetric{p.pp, value}
	return
}

// CsvMetic
type CsvMetric struct {
	pp     *Pool
	values []string
}

// GetString get the value as string
func (c *CsvMetric) GetString(key string, nullable bool) (val interface{}) {
	var idx int
	var ok bool
	if idx, ok = c.pp.csvFormat[key]; !ok || c.values[idx] == "null" {
		if nullable {
			return
		}
		val = ""
		return
	}
	val = c.values[idx]
	return
}

// GetFloat returns the value as float
func (c *CsvMetric) GetFloat(key string, nullable bool) (val interface{}) {
	var idx int
	var ok bool
	if idx, ok = c.pp.csvFormat[key]; !ok || c.values[idx] == "null" {
		if nullable {
			return
		}
		val = float64(0.0)
		return
	}
	val = fastfloat.ParseBestEffort(c.values[idx])
	return
}

func (c *CsvMetric) GetBool(key string, nullable bool) (val interface{}) {
	var idx int
	var ok bool
	if idx, ok = c.pp.csvFormat[key]; !ok || c.values[idx] == "" || c.values[idx] == "null" {
		if nullable {
			return
		}
		val = false
		return
	}
	val = (c.values[idx] == "true")
	return
}

func (c *CsvMetric) GetInt(key string, nullable bool) (val interface{}) {
	var idx int
	var ok bool
	if idx, ok = c.pp.csvFormat[key]; !ok || c.values[idx] == "null" {
		if nullable {
			return
		}
		val = int64(0)
		return
	}
	if s := c.values[idx]; s == "true" {
		val = int64(1)
	} else {
		val = fastfloat.ParseInt64BestEffort(s)
	}
	return
}

func (c *CsvMetric) GetDateTime(key string, nullable bool) (val interface{}) {
	var idx int
	var ok bool
	if idx, ok = c.pp.csvFormat[key]; !ok || c.values[idx] == "null" {
		if nullable {
			return
		}
		val = Epoch
		return
	}
	s := c.values[idx]
	if dd, err := strconv.ParseFloat(s, 64); err != nil {
		var err error
		if val, err = c.pp.ParseDateTime(key, s); err != nil {
			val = Epoch
		}
	} else {
		val = UnixFloat(dd, c.pp.timeUnit)
	}
	return
}

func (c *CsvMetric) GetElasticDateTime(key string, nullable bool) (val interface{}) {
	t := c.GetDateTime(key, nullable)
	if t != nil {
		val = t.(time.Time).Unix()
	}
	return
}

// GetArray parse an CSV encoded array
func (c *CsvMetric) GetArray(key string, typ int) (val interface{}) {
	s := c.GetString(key, false)
	str, _ := s.(string)
	if str == "" || str[0] != '[' {
		val = makeArray(typ)
		return
	}
	array := gjson.Parse(str).Array()
	if len(array) == 0 {
		val = makeArray(typ)
		return
	}
	switch typ {
	case model.Bool:
		results := make([]bool, 0, len(array))
		for _, e := range array {
			v := (e.Type == gjson.True)
			results = append(results, v)
		}
		val = results
	case model.Int:
		results := make([]int64, 0, len(array))
		for _, e := range array {
			var v int64
			switch e.Type {
			case gjson.True:
				v = int64(1)
			case gjson.Number:
				if v = e.Int(); float64(v) != e.Num {
					v = int64(0)
				}
			default:
				v = int64(0)
			}
			results = append(results, v)
		}
		val = results
	case model.Float:
		results := make([]float64, 0, len(array))
		for _, e := range array {
			var v float64
			switch e.Type {
			case gjson.Number:
				v = e.Num
			default:
				v = float64(0.0)
			}
			results = append(results, v)
		}
		val = results
	case model.String:
		results := make([]string, 0, len(array))
		for _, e := range array {
			var v string
			switch e.Type {
			case gjson.Null:
				v = ""
			case gjson.String:
				v = e.Str
			default:
				v = e.Raw
			}
			results = append(results, v)
		}
		val = results
	case model.DateTime:
		results := make([]time.Time, 0, len(array))
		for _, e := range array {
			var t time.Time
			switch e.Type {
			case gjson.Number:
				t = UnixFloat(e.Num, c.pp.timeUnit)
			case gjson.String:
				var err error
				if t, err = c.pp.ParseDateTime(key, e.Str); err != nil {
					t = Epoch
				}
			default:
				t = Epoch
			}
			results = append(results, t)
		}
		val = results
	default:
		util.Logger.Fatal(fmt.Sprintf("LOGIC ERROR: unsupported array type %v", typ))
	}
	return
}

func (c *CsvMetric) GetNewKeys(knownKeys, newKeys *sync.Map, white, black *regexp.Regexp) bool {
	return false
}
