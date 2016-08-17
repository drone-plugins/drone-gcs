package main

import "encoding/json"

// these types were copied from drone-plugins/drone-s3-sync

type DeepStringMapFlag struct {
	parts map[string]map[string]string
}

func (d *DeepStringMapFlag) String() string {
	return ""
}

func (d *DeepStringMapFlag) Get() map[string]map[string]string {
	return d.parts
}

func (d *DeepStringMapFlag) Set(value string) error {
	d.parts = map[string]map[string]string{}
	err := json.Unmarshal([]byte(value), &d.parts)
	if err != nil {
		single := map[string]string{}
		err := json.Unmarshal([]byte(value), &single)
		if err != nil {
			return err
		}

		d.parts["*"] = single
	}

	return nil
}

type StringMapFlag struct {
	parts map[string]string
}

func (s *StringMapFlag) String() string {
	return ""
}

func (s *StringMapFlag) Get() map[string]string {
	return s.parts
}

func (s *StringMapFlag) Set(value string) error {
	s.parts = map[string]string{}
	err := json.Unmarshal([]byte(value), &s.parts)
	if err != nil {
		s.parts["*"] = value
	}
	return nil
}

type MapFlag struct {
	parts map[string]string
}

func (m *MapFlag) String() string {
	return ""
}

func (m *MapFlag) Get() map[string]string {
	return m.parts
}

func (m *MapFlag) Set(value string) error {
	m.parts = map[string]string{}
	return json.Unmarshal([]byte(value), &m.parts)
}
