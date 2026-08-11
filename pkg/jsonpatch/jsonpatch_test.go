/*
Copyright 2025 The KubeEdge Authors.

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

package jsonpatch

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestJSONPatch(t *testing.T) {
	type beer struct {
		Number int    `json:"numb"`
		Str    string `json:"str"`
	}
	cases := []struct {
		name  string
		op    Operation
		path  string
		value any
		want  []map[string]any
	}{
		{
			name:  "add string",
			op:    OpAdd,
			path:  "/a/b/c",
			value: "123",
			want:  []map[string]any{{"op": "add", "path": "/a/b/c", "value": "123"}},
		},
		{
			name: "remove without value",
			op:   OpRemove,
			path: "/a/b/c",
			want: []map[string]any{{"op": "remove", "path": "/a/b/c"}},
		},
		{
			name: "add null",
			op:   OpAdd,
			path: "/a/b/c",
			want: []map[string]any{{"op": "add", "path": "/a/b/c", "value": nil}},
		},
		{
			name:  "replace integer",
			op:    OpReplace,
			path:  "/a/b/c",
			value: int32(1),
			want:  []map[string]any{{"op": "replace", "path": "/a/b/c", "value": float64(1)}},
		},
		{
			name:  "replace boolean",
			op:    OpReplace,
			path:  "/a/b/c",
			value: true,
			want:  []map[string]any{{"op": "replace", "path": "/a/b/c", "value": true}},
		},
		{
			name:  "replace number",
			op:    OpReplace,
			path:  "/a/b/c",
			value: 3.14,
			want:  []map[string]any{{"op": "replace", "path": "/a/b/c", "value": 3.14}},
		},
		{
			name:  "add object",
			op:    OpAdd,
			path:  "/a/b/c",
			value: beer{Number: 10, Str: "Hello"},
			want:  []map[string]any{{"op": "add", "path": "/a/b/c", "value": map[string]any{"numb": float64(10), "str": "Hello"}}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bff, err := New().Add(c.op, c.path, c.value).ToJSON()
			assert.NoError(t, err)

			var got []map[string]any
			assert.NoError(t, json.Unmarshal(bff, &got))
			assert.Equal(t, c.want, got)
		})
	}
}
