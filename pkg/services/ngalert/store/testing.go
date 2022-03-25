package store

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/grafana/grafana/pkg/services/annotations"
	"github.com/grafana/grafana/pkg/util"

	models2 "github.com/grafana/grafana/pkg/models"
	"github.com/grafana/grafana/pkg/services/ngalert/models"

	amv2 "github.com/prometheus/alertmanager/api/v2/models"
	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func NewFakeRuleStore(t *testing.T) *FakeRuleStore {
	return &FakeRuleStore{
		t:     t,
		Rules: map[int64][]*models.AlertRule{},
		Hook: func(interface{}) error {
			return nil
		},
		Folders: map[int64][]*models2.Folder{},
	}
}

// FakeRuleStore mocks the RuleStore of the scheduler.
type FakeRuleStore struct {
	t   *testing.T
	mtx sync.Mutex
	// OrgID -> RuleGroup -> Namespace -> Rules
	Rules       map[int64][]*models.AlertRule
	Hook        func(cmd interface{}) error // use Hook if you need to intercept some query and return an error
	RecordedOps []interface{}
	Folders     map[int64][]*models2.Folder
}

type GenericRecordedQuery struct {
	Name   string
	Params []interface{}
}

// PutRule puts the rule in the Rules map. If there are existing rule in the same namespace, they will be overwritten
func (f *FakeRuleStore) PutRule(_ context.Context, rules ...*models.AlertRule) {
	f.mtx.Lock()
	defer f.mtx.Unlock()
mainloop:
	for _, r := range rules {
		rgs := f.Rules[r.OrgID]
		for idx, rulePtr := range rgs {
			if rulePtr.UID == r.UID {
				rgs[idx] = r
				continue mainloop
			}
		}
		rgs = append(rgs, r)
		f.Rules[r.OrgID] = rgs

		var existing *models2.Folder
		folders := f.Folders[r.OrgID]
		for _, folder := range folders {
			if folder.Uid == r.NamespaceUID {
				existing = folder
				break
			}
		}
		if existing == nil {
			folders = append(folders, &models2.Folder{
				Id:    rand.Int63(),
				Uid:   r.NamespaceUID,
				Title: "TEST-FOLDER-" + util.GenerateShortUID(),
			})
			f.Folders[r.OrgID] = folders
		}
	}
}

// GetRecordedCommands filters recorded commands using predicate function. Returns the subset of the recorded commands that meet the predicate
func (f *FakeRuleStore) GetRecordedCommands(predicate func(cmd interface{}) (interface{}, bool)) []interface{} {
	f.mtx.Lock()
	defer f.mtx.Unlock()

	result := make([]interface{}, 0, len(f.RecordedOps))
	for _, op := range f.RecordedOps {
		cmd, ok := predicate(op)
		if !ok {
			continue
		}
		result = append(result, cmd)
	}
	return result
}

func (f *FakeRuleStore) DeleteAlertRulesByUID(_ context.Context, orgID int64, UIDs ...string) error {
	f.RecordedOps = append(f.RecordedOps, GenericRecordedQuery{
		Name:   "DeleteAlertRulesByUID",
		Params: []interface{}{orgID, UIDs},
	})

	rules := f.Rules[orgID]

	var result = make([]*models.AlertRule, 0, len(rules))

	for _, rule := range rules {
		add := true
		for _, UID := range UIDs {
			if rule.UID == UID {
				add = false
				break
			}
		}
		if add {
			result = append(result, rule)
		}
	}

	f.Rules[orgID] = result
	return nil
}

func (f *FakeRuleStore) DeleteAlertInstancesByRuleUID(_ context.Context, _ int64, _ string) error {
	return nil
}
func (f *FakeRuleStore) GetAlertRuleByUID(_ context.Context, q *models.GetAlertRuleByUIDQuery) error {
	f.mtx.Lock()
	defer f.mtx.Unlock()
	f.RecordedOps = append(f.RecordedOps, *q)
	if err := f.Hook(*q); err != nil {
		return err
	}
	rules, ok := f.Rules[q.OrgID]
	if !ok {
		return nil
	}

	for _, rule := range rules {
		if rule.UID == q.UID {
			q.Result = rule
			break
		}
	}
	return nil
}

// For now, we're not implementing namespace filtering.
func (f *FakeRuleStore) GetAlertRulesForScheduling(_ context.Context, q *models.ListAlertRulesQuery) error {
	f.mtx.Lock()
	defer f.mtx.Unlock()
	f.RecordedOps = append(f.RecordedOps, *q)
	if err := f.Hook(*q); err != nil {
		return err
	}
	for _, rules := range f.Rules {
		q.Result = append(q.Result, rules...)
	}
	return nil
}

func (f *FakeRuleStore) GetOrgAlertRules(_ context.Context, q *models.ListAlertRulesQuery) error {
	f.mtx.Lock()
	defer f.mtx.Unlock()
	f.RecordedOps = append(f.RecordedOps, *q)

	rules, ok := f.Rules[q.OrgID]
	if !ok {
		return nil
	}
	q.Result = rules
	return nil
}
func (f *FakeRuleStore) GetNamespaceAlertRules(_ context.Context, q *models.ListNamespaceAlertRulesQuery) error {
	f.mtx.Lock()
	defer f.mtx.Unlock()
	f.RecordedOps = append(f.RecordedOps, *q)
	return nil
}
func (f *FakeRuleStore) GetAlertRules(_ context.Context, q *models.GetAlertRulesQuery) error {
	f.mtx.Lock()
	defer f.mtx.Unlock()
	f.RecordedOps = append(f.RecordedOps, *q)
	if err := f.Hook(*q); err != nil {
		return err
	}
	rules, ok := f.Rules[q.OrgID]
	if !ok {
		return nil
	}
	var result []*models.AlertRule
	for _, rule := range rules {
		if q.NamespaceUID != rule.NamespaceUID {
			continue
		}
		if q.RuleGroup != nil && *q.RuleGroup != rule.RuleGroup {
			continue
		}
		result = append(result, rule)
	}
	q.Result = result
	return nil
}
func (f *FakeRuleStore) GetNamespaces(_ context.Context, orgID int64, _ *models2.SignedInUser) (map[string]*models2.Folder, error) {
	f.mtx.Lock()
	defer f.mtx.Unlock()

	namespacesMap := map[string]*models2.Folder{}

	_, ok := f.Rules[orgID]
	if !ok {
		return namespacesMap, nil
	}

	for _, rule := range f.Rules[orgID] {
		namespacesMap[rule.NamespaceUID] = &models2.Folder{}
	}
	return namespacesMap, nil
}
func (f *FakeRuleStore) GetNamespaceByTitle(_ context.Context, title string, orgID int64, _ *models2.SignedInUser, _ bool) (*models2.Folder, error) {
	folders := f.Folders[orgID]
	for _, folder := range folders {
		if folder.Title == title {
			return folder, nil
		}
	}
	return nil, fmt.Errorf("not found")
}
func (f *FakeRuleStore) GetOrgRuleGroups(_ context.Context, q *models.ListOrgRuleGroupsQuery) error {
	f.mtx.Lock()
	defer f.mtx.Unlock()
	f.RecordedOps = append(f.RecordedOps, *q)
	if err := f.Hook(*q); err != nil {
		return err
	}

	// If we have namespaces, we want to try and retrieve the list of rules stored.
	if len(q.NamespaceUIDs) != 0 {
		rules, ok := f.Rules[q.OrgID]
		if !ok {
			return nil
		}

		var ruleGroups [][]string
		for _, rule := range rules {
			for _, namespace := range q.NamespaceUIDs {
				if rule.NamespaceUID == namespace { // if they match, they should go in.
					ruleGroups = append(ruleGroups, []string{rule.RuleGroup, rule.NamespaceUID, rule.NamespaceUID})
				}
			}
		}

		q.Result = ruleGroups
	}
	return nil
}

func (f *FakeRuleStore) UpsertAlertRules(_ context.Context, q []UpsertRule) error {
	f.mtx.Lock()
	defer f.mtx.Unlock()
	f.RecordedOps = append(f.RecordedOps, q)
	if err := f.Hook(q); err != nil {
		return err
	}
	return nil
}

func (f *FakeRuleStore) InTransaction(ctx context.Context, fn func(c context.Context) error) error {
	return fn(ctx)
}

type FakeInstanceStore struct {
	mtx         sync.Mutex
	RecordedOps []interface{}
}

func (f *FakeInstanceStore) GetAlertInstance(_ context.Context, q *models.GetAlertInstanceQuery) error {
	f.mtx.Lock()
	defer f.mtx.Unlock()
	f.RecordedOps = append(f.RecordedOps, *q)
	return nil
}
func (f *FakeInstanceStore) ListAlertInstances(_ context.Context, q *models.ListAlertInstancesQuery) error {
	f.mtx.Lock()
	defer f.mtx.Unlock()
	f.RecordedOps = append(f.RecordedOps, *q)
	return nil
}
func (f *FakeInstanceStore) SaveAlertInstance(_ context.Context, q *models.SaveAlertInstanceCommand) error {
	f.mtx.Lock()
	defer f.mtx.Unlock()
	f.RecordedOps = append(f.RecordedOps, *q)
	return nil
}

func (f *FakeInstanceStore) FetchOrgIds(_ context.Context) ([]int64, error) { return []int64{}, nil }
func (f *FakeInstanceStore) DeleteAlertInstance(_ context.Context, _ int64, _, _ string) error {
	return nil
}

func NewFakeAdminConfigStore(t *testing.T) *FakeAdminConfigStore {
	t.Helper()
	return &FakeAdminConfigStore{Configs: map[int64]*models.AdminConfiguration{}}
}

type FakeAdminConfigStore struct {
	mtx     sync.Mutex
	Configs map[int64]*models.AdminConfiguration
}

func (f *FakeAdminConfigStore) GetAdminConfiguration(orgID int64) (*models.AdminConfiguration, error) {
	f.mtx.Lock()
	defer f.mtx.Unlock()
	return f.Configs[orgID], nil
}

func (f *FakeAdminConfigStore) GetAdminConfigurations() ([]*models.AdminConfiguration, error) {
	f.mtx.Lock()
	defer f.mtx.Unlock()
	acs := make([]*models.AdminConfiguration, 0, len(f.Configs))
	for _, ac := range f.Configs {
		acs = append(acs, ac)
	}

	return acs, nil
}

func (f *FakeAdminConfigStore) DeleteAdminConfiguration(orgID int64) error {
	f.mtx.Lock()
	defer f.mtx.Unlock()
	delete(f.Configs, orgID)
	return nil
}
func (f *FakeAdminConfigStore) UpdateAdminConfiguration(cmd UpdateAdminConfigurationCmd) error {
	f.mtx.Lock()
	defer f.mtx.Unlock()
	f.Configs[cmd.AdminConfiguration.OrgID] = cmd.AdminConfiguration

	return nil
}

type FakeExternalAlertmanager struct {
	t      *testing.T
	mtx    sync.Mutex
	alerts amv2.PostableAlerts
	Server *httptest.Server
}

func NewFakeExternalAlertmanager(t *testing.T) *FakeExternalAlertmanager {
	t.Helper()

	am := &FakeExternalAlertmanager{
		t:      t,
		alerts: amv2.PostableAlerts{},
	}
	am.Server = httptest.NewServer(http.HandlerFunc(am.Handler()))

	return am
}

func (am *FakeExternalAlertmanager) URL() string {
	return am.Server.URL
}

func (am *FakeExternalAlertmanager) AlertNamesCompare(expected []string) bool {
	n := []string{}
	alerts := am.Alerts()

	if len(expected) != len(alerts) {
		return false
	}

	for _, a := range am.Alerts() {
		for k, v := range a.Alert.Labels {
			if k == model.AlertNameLabel {
				n = append(n, v)
			}
		}
	}

	return assert.ObjectsAreEqual(expected, n)
}

func (am *FakeExternalAlertmanager) AlertsCount() int {
	am.mtx.Lock()
	defer am.mtx.Unlock()

	return len(am.alerts)
}

func (am *FakeExternalAlertmanager) Alerts() amv2.PostableAlerts {
	am.mtx.Lock()
	defer am.mtx.Unlock()
	return am.alerts
}

func (am *FakeExternalAlertmanager) Handler() func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		b, err := ioutil.ReadAll(r.Body)
		require.NoError(am.t, err)

		a := amv2.PostableAlerts{}
		require.NoError(am.t, json.Unmarshal(b, &a))

		am.mtx.Lock()
		am.alerts = append(am.alerts, a...)
		am.mtx.Unlock()
	}
}

func (am *FakeExternalAlertmanager) Close() {
	am.Server.Close()
}

type FakeAnnotationsRepo struct {
	mtx   sync.Mutex
	Items []*annotations.Item
}

func NewFakeAnnotationsRepo() *FakeAnnotationsRepo {
	return &FakeAnnotationsRepo{
		Items: make([]*annotations.Item, 0),
	}
}

func (repo *FakeAnnotationsRepo) Len() int {
	repo.mtx.Lock()
	defer repo.mtx.Unlock()
	return len(repo.Items)
}

func (repo *FakeAnnotationsRepo) Delete(params *annotations.DeleteParams) error {
	return nil
}

func (repo *FakeAnnotationsRepo) Save(item *annotations.Item) error {
	repo.mtx.Lock()
	defer repo.mtx.Unlock()
	repo.Items = append(repo.Items, item)

	return nil
}
func (repo *FakeAnnotationsRepo) Update(_ context.Context, item *annotations.Item) error {
	return nil
}

func (repo *FakeAnnotationsRepo) Find(_ context.Context, query *annotations.ItemQuery) ([]*annotations.ItemDTO, error) {
	annotations := []*annotations.ItemDTO{{Id: 1}}
	return annotations, nil
}

func (repo *FakeAnnotationsRepo) FindTags(query *annotations.TagsQuery) (annotations.FindTagsResult, error) {
	result := annotations.FindTagsResult{
		Tags: []*annotations.TagsDTO{},
	}
	return result, nil
}
