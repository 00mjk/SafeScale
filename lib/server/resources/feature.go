/*
 * Copyright 2018-2020, CS Systemes d'Information, http://www.c-s.fr
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package resources

import (
	"github.com/spf13/viper"

	"github.com/CS-SI/SafeScale/lib/server/resources/enums/featuretargettype"
	"github.com/CS-SI/SafeScale/lib/server/resources/enums/installmethod"
	"github.com/CS-SI/SafeScale/lib/utils/concurrency"
	"github.com/CS-SI/SafeScale/lib/utils/data"
	"github.com/CS-SI/SafeScale/lib/utils/fail"
)

// Targetable is an interface that target must satisfy to be able to install something on it
type Targetable interface {
	data.Identifiable

	// GetTargetType returns the type of the target
	SafeGetTargetType() featuretargettype.Enum
	// GetInstallMethods returns a list of installation methods useable on the target, ordered from upper to lower preference (1 = highest preference)
	SafeGetInstallMethods(concurrency.Task) map[uint8]installmethod.Enum
	// GetInstalledFatures returns a list of installed features
	SafeGetInstalledFeatures(concurrency.Task) []string
	// ComplementFeatureParameters adds parameters corresponding to the target in preparation of feature installation
	ComplementFeatureParameters(t concurrency.Task, v data.Map) fail.Error
}

// Feature defines the interface of feature
type Feature interface {
	data.Clonable

	// GetName returns the name of the feature
	GetName() (string, fail.Error)
	// GetFilename returns the filename of the feature
	GetFilename() (string, fail.Error)
	// GetDisplayFilename displays the filename of display (optionally adding '[embedded]' for embedded features)
	GetDisplayFilename() (string, fail.Error)
	// SafeGetName returns the name of the feature
	SafeGetName() string
	// SafeGetFilename returns the filename of the feature
	SafeGetFilename() string
	// GetDisplayFilename displays the filename of display (optionally adding '[embedded]' for embedded features)
	SafeGetDisplayFilename() string
	// GetSpecs returns the feature specs
	SafeGetSpecs() *viper.Viper
	// Requirements returns the other features needed as requirements
	GetRequirements() ([]string, fail.Error)
	// Applyable tells if the feature is installable on the target
	Applyable(Targetable) bool
	// Check if feature is installed on target
	Check(t Targetable, v data.Map, fs FeatureSettings) (Results, fail.Error)
	// Add installs the feature on the target
	Add(t Targetable, v data.Map, fs FeatureSettings) (Results, fail.Error)
	// Remove uninstalls the feature from the target
	Remove(t Targetable, v data.Map, fs FeatureSettings) (Results, fail.Error)
}

// FeatureSettings are used to tune the feature
type FeatureSettings struct {
	// SkipProxy to tell not to try to set reverse proxy
	SkipProxy bool
	// Serialize force not to parallel hosts in step
	Serialize bool
	// SkipFeatureRequirements tells not to install required features
	SkipFeatureRequirements bool
	// SkipSizingRequirements tells not to check sizing requirements
	SkipSizingRequirements bool
	// AddUnconditionally tells to not check before addition (no effect for check or removal)
	AddUnconditionally bool
}
