package state

import (
	"fmt"
	"strings"
	"sync"

	"github.com/grafana/grafana-plugin-sdk-go/data"

	"github.com/grafana/grafana/pkg/infra/log"
	"github.com/grafana/grafana/pkg/services/ngalert/eval"
	"github.com/grafana/grafana/pkg/services/ngalert/metrics"
	ngModels "github.com/grafana/grafana/pkg/services/ngalert/models"
	prometheusModel "github.com/prometheus/common/model"
)

type cache struct {
	states    map[string]*State
	mtxStates sync.RWMutex
	log       log.Logger
	metrics   *metrics.Metrics
}

func newCache(logger log.Logger, metrics *metrics.Metrics) *cache {
	return &cache{
		states:  make(map[string]*State),
		log:     logger,
		metrics: metrics,
	}
}

func (c *cache) getOrCreate(alertRule *ngModels.AlertRule, result eval.Result) *State {
	c.mtxStates.Lock()
	defer c.mtxStates.Unlock()

	// if duplicate labels exist, alertRule label will take precedence
	lbs := mergeLabels(alertRule.Labels, result.Instance)
	lbs[ngModels.UIDLabel] = alertRule.UID
	lbs[ngModels.NamespaceUIDLabel] = alertRule.NamespaceUID
	lbs[prometheusModel.AlertNameLabel] = alertRule.Title

	il := ngModels.InstanceLabels(lbs)
	id, err := il.StringKey()
	if err != nil {
		c.log.Error("error getting cacheId for entry", "msg", err.Error())
	}

	if state, ok := c.states[id]; ok {
		return state
	}

	annotations := map[string]string{}
	if len(alertRule.Annotations) > 0 {
		annotations = alertRule.Annotations
	}

	// If the first result we get is alerting, set StartsAt to EvaluatedAt because we
	// do not have data for determining StartsAt otherwise
	newState := &State{
		AlertRuleUID:       alertRule.UID,
		OrgID:              alertRule.OrgID,
		CacheId:            id,
		Labels:             lbs,
		State:              result.State,
		Annotations:        annotations,
		EvaluationDuration: result.EvaluationDuration,
	}
	if result.State == eval.Alerting {
		newState.StartsAt = result.EvaluatedAt
	}
	c.states[id] = newState
	return newState
}

func (c *cache) set(entry *State) {
	c.mtxStates.Lock()
	defer c.mtxStates.Unlock()
	c.states[entry.CacheId] = entry
}

func (c *cache) get(id string) (*State, error) {
	c.mtxStates.Lock()
	defer c.mtxStates.Unlock()
	if state, ok := c.states[id]; ok {
		return state, nil
	}
	return nil, fmt.Errorf("no entry for id: %s", id)
}

func (c *cache) getAll() []*State {
	var states []*State
	c.mtxStates.Lock()
	defer c.mtxStates.Unlock()
	for _, v := range c.states {
		states = append(states, v)
	}
	return states
}

func (c *cache) getStatesByRuleUID() map[string][]*State {
	ruleMap := make(map[string][]*State)
	c.mtxStates.Lock()
	defer c.mtxStates.Unlock()
	for _, state := range c.states {
		if ruleStates, ok := ruleMap[state.AlertRuleUID]; ok {
			ruleStates = append(ruleStates, state)
			ruleMap[state.AlertRuleUID] = ruleStates
		} else {
			ruleStates := []*State{state}
			ruleMap[state.AlertRuleUID] = ruleStates
		}
	}
	return ruleMap
}

func (c *cache) reset() {
	c.mtxStates.Lock()
	defer c.mtxStates.Unlock()
	c.states = make(map[string]*State)
}

func (c *cache) trim() {
	c.mtxStates.Lock()
	defer c.mtxStates.Unlock()

	ct := make(map[eval.State]int)

	for _, v := range c.states {
		if len(v.Results) > 100 {
			newResults := make([]Evaluation, 100)
			// Keep last 100 results
			copy(newResults, v.Results[len(v.Results)-100:])
			v.Results = newResults
		}

		n := ct[v.State]
		ct[v.State] = n + 1
	}

	for k, n := range ct {
		c.metrics.AlertState.WithLabelValues(strings.ToLower(k.String())).Set(float64(n))
	}
}

// if duplicate labels exist, keep the value from the first set
func mergeLabels(a, b data.Labels) data.Labels {
	newLbs := data.Labels{}
	for k, v := range a {
		newLbs[k] = v
	}
	for k, v := range b {
		if _, ok := newLbs[k]; !ok {
			newLbs[k] = v
		}
	}
	return newLbs
}
