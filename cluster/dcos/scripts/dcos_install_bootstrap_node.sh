#!/usr/bin/env bash
#
# Copyright 2015-2018 CS Systemes d'Information
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
#
# Deploys a DCOS bootstrap/upgrade server with minimum requirements
#
# This script has to be executed on the bootstrap/upgrade server


# Installs and configures everything needed on any node
{{.CommonConfigurationScript}}

# Prepares the folder to contain cluster Bootstrap/Upgrade data
mkdir -p /usr/local/dcos/

# Install DCOS environment
mkdir -p /usr/local/dcos/genconf
cd /usr/local/dcos

cat genconf/config.yaml <<- EOF
---
bootstrap_url: http://{{.BootstrapIP}}:{{.BootstrapPort}}
cluster_name: {{.ClusterName}}
exhibitor_storage_backend: static
master_discovery: static
ip_detect_public_filename: genconf/ip-detect-public.sh
master_list:
{{range .MasterIPs}}- {{.}}{{end}}
{{if .DNSServerIPs}}
resolvers:
{{range .DNSServerIPs}}- {{.}}{{end}}
{{end}}
use_proxy: 'false'
#use_proxy: 'true'
#http_proxy: http://{{.HTTPProxyHost}}:{{.HTTPProxyPort}}
#https_proxy: https://{{.HTTPSProxyHost}}:{{.HTTPSProxyPortr}}
#no_proxy:
#- 'foo.bar.com'
#- '.baz.com'
EOF

cat genconf/ip-detect-public.sh <<- EOF
#!/usr/bin/env bash
set -o nounset -o errexit
export PATH=/usr/sbin:/usr/bin:$PATH
echo $(ip addr show eth0 | grep -Eo '[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}' | head -1)
EOF

[ ! -f dcos_generate_config.sh ] && wget
sudo bash dcos_generate_config.sh

exit 0
