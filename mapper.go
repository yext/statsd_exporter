// Copyright 2013 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"fmt"
	"io/ioutil"
	"regexp"
	"strings"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	yaml "gopkg.in/yaml.v2"
)

var (
	identifierRE   = `[a-zA-Z_][a-zA-Z0-9_]+`
	statsdMetricRE = `[a-zA-Z_](-?[a-zA-Z0-9_])+`

	metricLineRE = regexp.MustCompile(`^(\*\.|` + statsdMetricRE + `\.)+(\*|` + statsdMetricRE + `)$`)
	labelLineRE  = regexp.MustCompile(`^(` + identifierRE + `)\s*=\s*"(.*)"$`)
	metricNameRE = regexp.MustCompile(`^` + identifierRE + `$`)
)

type mapperConfigDefaults struct {
	TimerType timerType `yaml:"timer_type"`
	Buckets   []float64 `yaml:"buckets"`
	MatchType matchType `yaml:"match_type"`
}

type metricMapper struct {
	Defaults mapperConfigDefaults `yaml:"defaults"`
	Mappings []metricMapping      `yaml:"mappings"`
	mutex    sync.Mutex
}

type metricMapping struct {
	Match     string `yaml:"match"`
	regex     *regexp.Regexp
	Labels    prometheus.Labels `yaml:"labels"`
	TimerType timerType         `yaml:"timer_type"`
	Buckets   []float64         `yaml:"buckets"`
	MatchType matchType         `yaml:"match_type"`
}

func (m *metricMapper) initFromYAMLString(fileContents string) error {
	var n metricMapper

	if err := yaml.Unmarshal([]byte(fileContents), &n); err != nil {
		return err
	}

	if n.Defaults.Buckets == nil || len(n.Defaults.Buckets) == 0 {
		n.Defaults.Buckets = prometheus.DefBuckets
	}

	if n.Defaults.MatchType == matchTypeDefault {
		n.Defaults.MatchType = matchTypeGlob
	}

	for i := range n.Mappings {
		currentMapping := &n.Mappings[i]

		// check that label is correct
		for k, v := range currentMapping.Labels {
			if !metricNameRE.MatchString(k) {
				return fmt.Errorf("invalid label key: %s", k)
			}
			if k == "name" && !metricNameRE.MatchString(v) {
				return fmt.Errorf("metric name '%s' doesn't match regex '%s'", v, metricNameRE)
			}
		}

		if _, ok := currentMapping.Labels["name"]; !ok {
			return fmt.Errorf("line %d: metric mapping didn't set a metric name", i)
		}
		if currentMapping.MatchType == "" {
			currentMapping.MatchType = n.Defaults.MatchType
		}

		if currentMapping.MatchType == matchTypeGlob {
			if !metricLineRE.MatchString(currentMapping.Match) {
				return fmt.Errorf("invalid match: %s", currentMapping.Match)
			}
			// Translate the glob-style metric match line into a proper regex that we
			// can use to match metrics later on.
			metricRe := strings.Replace(currentMapping.Match, ".", "\\.", -1)
			metricRe = strings.Replace(metricRe, "*", "([^.]+)", -1)
			currentMapping.regex = regexp.MustCompile("^" + metricRe + "$")
		} else {
			currentMapping.regex = regexp.MustCompile(currentMapping.Match)
		}

		if currentMapping.TimerType == "" {
			currentMapping.TimerType = n.Defaults.TimerType
		}

		if currentMapping.Buckets == nil || len(currentMapping.Buckets) == 0 {
			currentMapping.Buckets = n.Defaults.Buckets
		}

	}

	m.mutex.Lock()
	defer m.mutex.Unlock()

	m.Defaults = n.Defaults
	m.Mappings = n.Mappings

	mappingsCount.Set(float64(len(n.Mappings)))

	return nil
}

func (m *metricMapper) initFromFile(fileName string) error {
	mappingStr, err := ioutil.ReadFile(fileName)
	if err != nil {
		return err
	}
	return m.initFromYAMLString(string(mappingStr))
}

func (m *metricMapper) getMapping(statsdMetric string) (*metricMapping, prometheus.Labels, bool) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	for _, mapping := range m.Mappings {
		matches := mapping.regex.FindStringSubmatchIndex(statsdMetric)
		if len(matches) == 0 {
			continue
		}

		labels := prometheus.Labels{}
		for label, valueExpr := range mapping.Labels {
			value := mapping.regex.ExpandString([]byte{}, valueExpr, statsdMetric, matches)
			labels[label] = string(value)
		}
		return &mapping, labels, true
	}

	return nil, nil, false
}
