// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2022 Datadog, Inc.

//go:build appsec
// +build appsec

package appsec

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"gopkg.in/DataDog/dd-trace-go.v1/internal/log"
	"gopkg.in/DataDog/dd-trace-go.v1/internal/remoteconfig"

	rc "github.com/DataDog/datadog-agent/pkg/remoteconfig/state"
	waf "github.com/DataDog/go-libddwaf"
)

func genApplyStatus(ack bool, err error) rc.ApplyStatus {
	status := rc.ApplyStatus{
		State: rc.ApplyStateUnacknowledged,
	}
	if err != nil {
		status.State = rc.ApplyStateError
		status.Error = err.Error()
	} else if ack {
		status.State = rc.ApplyStateAcknowledged
	}

	return status
}

func statusesFromUpdate(u remoteconfig.ProductUpdate, ack bool, err error) map[string]rc.ApplyStatus {
	statuses := make(map[string]rc.ApplyStatus, len(u))
	for path := range u {
		statuses[path] = genApplyStatus(ack, err)
	}
	return statuses
}

func mergeMaps[K comparable, V any](m1 map[K]V, m2 map[K]V) map[K]V {
	merged := make(map[K]V)
	for key, value := range m1 {
		merged[key] = value
	}
	for key, value := range m2 {
		merged[key] = value
	}
	return merged
}

// onRCUpdate is called when one or several remote configuration updates are available
func (a *appsec) onRCUpdate(updates map[string]remoteconfig.ProductUpdate) map[string]rc.ApplyStatus {
	r := a.cfg.rulesManager.clone()
	statuses := map[string]rc.ApplyStatus{}
	// Process remote activation update separately
	// This makes it easier to short circuit the following loop that focuses on rules related updates
	if u, ok := updates[rc.ProductASMFeatures]; ok {
		statuses = mergeMaps(statuses, a.handleASMFeatures(u))
		delete(updates, rc.ProductASMFeatures)
	}

	// Process rules related updates
	for p, u := range updates {
		switch p {
		case rc.ProductASMData:
			// Merge all rules data entries together and store them as a rulesManager edit entry
			rulesData, status := mergeRulesData(u)
			statuses = mergeMaps(statuses, status)
			r.addEdit("asmdata", rulesFragment{RulesData: rulesData})
		case rc.ProductASMDD:
			// Switch the base rules of the rulesManager if the config received through ASM_DD is valid
			// If the config was removed, switch back to the static recommended rules
			if len(u) > 1 { // Don't process configs if more than one is received for ASM_DD
				log.Debug("appsec: Remote config: more than one config received for ASM_DD. Updates won't be applied")
				statuses = mergeMaps(statuses, statusesFromUpdate(u, true, errors.New("More than one config received for ASM_DD")))
				continue
			}
			for path, data := range u {
				if data == nil {
					log.Debug("appsec: Remote config: ASM_DD config removed. Switching back to default rules")
					r.changeBase(defaultRulesFragment(), "")
					break
				}
				var newBase rulesFragment
				if err := json.Unmarshal(data, &newBase); err != nil {
					log.Debug("appsec: Remote config: could not unmarshall ASM_DD rules: %v", err)
					statuses[path] = genApplyStatus(true, err)
					break
				}
				log.Debug("appsec: Remote config: switching to %s as the base rules file", path)
				r.changeBase(newBase, path)
				statuses[path] = genApplyStatus(true, nil)
			}
		case rc.ProductASM:
			// Store each config received through ASM as an edit entry in the rulesManager
			// Those entries will get merged together when the final rules are compiled
			// If a config gets removed, the rulesManager edit entry gets removed as well
			for path, data := range u {
				log.Debug("appsec: Remote config: processing the %s ASM config", path)
				statuses[path] = genApplyStatus(true, nil)
				if data == nil {
					log.Debug("appsec: Remote config: ASM config %s was removed", path)
					r.removeEdit(path)
					continue
				}
				var f rulesFragment
				if err := json.Unmarshal(data, &f); err != nil || !f.validate() {
					log.Debug("appsec: Remote config: error processing ASM config %s: %v", path, err)
					statuses[path] = genApplyStatus(true, err)
				} else {
					r.addEdit(path, f)
				}
			}
		default:
			log.Debug("appsec: Remote config: ignoring unsubscribed product %s", p)
		}
	}

	// Compile the final rules once all updates have been processed
	r.compile()
	data := r.raw()
	log.Debug("appsec: Remote config: final compiled rules: %s", data)
	if err := a.swapWAF(data); err != nil {
		log.Error("appsec: Remote config: could not apply the new security rules: %v", err)
		for k := range statuses {
			statuses[k] = genApplyStatus(true, err)
		}
	} else {
		a.cfg.rulesManager = r
	}
	return statuses
}

// handleASMFeatures deserializes an ASM_FEATURES configuration received through remote config
// and starts/stops appsec accordingly.
func (a *appsec) handleASMFeatures(u remoteconfig.ProductUpdate) map[string]rc.ApplyStatus {
	statuses := statusesFromUpdate(u, false, nil)
	if l := len(u); l > 1 {
		log.Error("appsec: Remote config: %d configs received for ASM_FEATURES. Expected one at most, returning early", l)
		return statuses
	}
	for path, raw := range u {
		var data rc.ASMFeaturesData
		status := rc.ApplyStatus{State: rc.ApplyStateAcknowledged}
		var err error
		log.Debug("appsec: Remote config: processing %s", path)

		// A nil config means ASM was disabled, and we stopped receiving the config file
		// Don't ack the config in this case and return early
		if raw == nil {
			log.Debug("appsec: Remote config: Stopping AppSec")
			a.stop()
			return statuses
		}
		if err = json.Unmarshal(raw, &data); err != nil {
			log.Error("appsec: Remote config: error while unmarshalling %s: %v. Configuration won't be applied.", path, err)
		} else if data.ASM.Enabled && !a.started {
			log.Debug("appsec: Remote config: Starting AppSec")
			if err = a.start(); err != nil {
				log.Error("appsec: Remote config: error while processing %s. Configuration won't be applied: %v", path, err)
			}
		} else if !data.ASM.Enabled && a.started {
			log.Debug("appsec: Remote config: Stopping AppSec")
			a.stop()
		}
		if err != nil {
			status = genApplyStatus(false, err)
		}
		statuses[path] = status
	}

	return statuses
}

func mergeRulesData(u remoteconfig.ProductUpdate) ([]ruleDataEntry, map[string]rc.ApplyStatus) {
	// Following the RFC, merging should only happen when two rules data with the same ID and same Type are received
	// allRulesData[ID][Type] will return the rules data of said id and type, if it exists
	allRulesData := make(map[string]map[string]ruleDataEntry)
	statuses := statusesFromUpdate(u, true, nil)

	for path, raw := range u {
		log.Debug("appsec: Remote config: processing %s", path)

		// A nil config means ASM_DATA was disabled, and we stopped receiving the config file
		// Don't ack the config in this case
		if raw == nil {
			log.Debug("appsec: remote config: %s disabled", path)
			statuses[path] = genApplyStatus(false, nil)
			continue
		}

		var rulesData rulesData
		if err := json.Unmarshal(raw, &rulesData); err != nil {
			log.Debug("appsec: Remote config: error while unmarshalling payload for %s: %v. Configuration won't be applied.", path, err)
			statuses[path] = genApplyStatus(false, err)
			continue
		}

		// Check each entry against allRulesData to see if merging is necessary
		for _, ruleData := range rulesData.RulesData {
			if allRulesData[ruleData.ID] == nil {
				allRulesData[ruleData.ID] = make(map[string]ruleDataEntry)
			}
			if data, ok := allRulesData[ruleData.ID][ruleData.Type]; ok {
				// Merge rules data entries with the same ID and Type
				data.Data = mergeRulesDataEntries(data.Data, ruleData.Data)
				allRulesData[ruleData.ID][ruleData.Type] = data
			} else {
				allRulesData[ruleData.ID][ruleData.Type] = ruleData
			}
		}
	}

	// Aggregate all the rules data before passing it over to the WAF
	var rulesData []ruleDataEntry
	for _, m := range allRulesData {
		for _, data := range m {
			rulesData = append(rulesData, data)
		}
	}
	return rulesData, statuses
}

// mergeRulesDataEntries merges two slices of rules data entries together, removing duplicates and
// only keeping the longest expiration values for similar entries.
func mergeRulesDataEntries(entries1, entries2 []rc.ASMDataRuleDataEntry) []rc.ASMDataRuleDataEntry {
	mergeMap := map[string]int64{}

	for _, entry := range entries1 {
		mergeMap[entry.Value] = entry.Expiration
	}
	// Replace the entry only if the new expiration timestamp goes later than the current one
	// If no expiration timestamp was provided (default to 0), then the data doesn't expire
	for _, entry := range entries2 {
		if exp, ok := mergeMap[entry.Value]; !ok || entry.Expiration == 0 || entry.Expiration > exp {
			mergeMap[entry.Value] = entry.Expiration
		}
	}
	// Create the final slice and return it
	entries := make([]rc.ASMDataRuleDataEntry, 0, len(mergeMap))
	for val, exp := range mergeMap {
		entries = append(entries, rc.ASMDataRuleDataEntry{
			Value:      val,
			Expiration: exp,
		})
	}
	return entries
}

func (a *appsec) startRC() {
	if a.rc != nil {
		a.rc.RegisterCallback(a.onRCUpdate)
		a.rc.Start()
	}
}

func (a *appsec) stopRC() {
	if a.rc != nil {
		a.rc.Stop()
	}
}

func (a *appsec) registerRCProduct(p string) error {
	if a.rc == nil {
		return fmt.Errorf("no valid remote configuration client")
	}
	a.cfg.rc.Products[p] = struct{}{}
	a.rc.RegisterProduct(p)
	return nil
}

func (a *appsec) unregisterRCProduct(p string) error {
	if a.rc == nil {
		return fmt.Errorf("no valid remote configuration client")
	}
	delete(a.cfg.rc.Products, p)
	a.rc.UnregisterProduct(p)
	return nil
}

func (a *appsec) registerRCCapability(c remoteconfig.Capability) error {
	a.cfg.rc.Capabilities[c] = struct{}{}
	if a.rc == nil {
		return fmt.Errorf("no valid remote configuration client")
	}
	a.rc.RegisterCapability(c)
	return nil
}

func (a *appsec) unregisterRCCapability(c remoteconfig.Capability) {
	if a.rc == nil {
		log.Debug("appsec: Remote config: no valid remote configuration client")
		return
	}
	delete(a.cfg.rc.Capabilities, c)
	a.rc.UnregisterCapability(c)
}

func (a *appsec) enableRemoteActivation() error {
	if a.rc == nil {
		return fmt.Errorf("no valid remote configuration client")
	}
	// First verify that the WAF is in good health. We perform this check in order not to falsely "allow" users to
	// activate ASM through remote config if activation would fail when trying to register a WAF handle
	// (ex: if the service runs on an unsupported platform).
	if err := waf.Health(); err != nil {
		log.Debug("appsec: WAF health check failed, remote activation will be disabled: %v", err)
		return err
	}
	a.registerRCProduct(rc.ProductASMFeatures)
	a.registerRCCapability(remoteconfig.ASMActivation)
	return nil
}

func (a *appsec) enableRCBlocking() {
	if a.rc == nil {
		log.Debug("appsec: Remote config: no valid remote configuration client")
		return
	}

	a.registerRCProduct(rc.ProductASM)
	a.registerRCProduct(rc.ProductASMDD)
	a.registerRCProduct(rc.ProductASMData)
	a.registerRCCapability(remoteconfig.ASMUserBlocking)
	a.registerRCCapability(remoteconfig.ASMRequestBlocking)

	if _, isSet := os.LookupEnv(rulesEnvVar); !isSet {
		a.registerRCCapability(remoteconfig.ASMIPBlocking)
		a.registerRCCapability(remoteconfig.ASMDDRules)
		a.registerRCCapability(remoteconfig.ASMExclusions)
	}
}

func (a *appsec) disableRCBlocking() {
	if a.rc == nil {
		return
	}
	a.unregisterRCCapability(remoteconfig.ASMDDRules)
	a.unregisterRCCapability(remoteconfig.ASMExclusions)
	a.unregisterRCCapability(remoteconfig.ASMIPBlocking)
	a.unregisterRCCapability(remoteconfig.ASMRequestBlocking)
	a.unregisterRCCapability(remoteconfig.ASMUserBlocking)
}
