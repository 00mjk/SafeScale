/*
 * Copyright 2018-2020, CS Systemes d'Information, http://csgroup.eu
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

package server

// TODO NOTICE Side-effects imports here

// This file is used to automatically register all providers
import (
	_ "github.com/CS-SI/SafeScale/lib/server/iaas/providers/aws"            // Imported to initialise tenants
	_ "github.com/CS-SI/SafeScale/lib/server/iaas/providers/cloudferro"     // Imported to initialise tenants
	_ "github.com/CS-SI/SafeScale/lib/server/iaas/providers/flexibleengine" // Imported to initialise tenants
	_ "github.com/CS-SI/SafeScale/lib/server/iaas/providers/gcp"            // Imported to initialise tenants
	_ "github.com/CS-SI/SafeScale/lib/server/iaas/providers/local"          // Imported to initialise tenants
	_ "github.com/CS-SI/SafeScale/lib/server/iaas/providers/openstack"      // Imported to initialise tenants
	_ "github.com/CS-SI/SafeScale/lib/server/iaas/providers/opentelekom"    // Imported to initialise tenants
	_ "github.com/CS-SI/SafeScale/lib/server/iaas/providers/outscale"       // Imported to initialise tenants
	_ "github.com/CS-SI/SafeScale/lib/server/iaas/providers/ovh"            // Imported to initialise tenants
)
