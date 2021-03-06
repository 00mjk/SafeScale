#!/usr/bin/env bash
#
# Copyright 2018, CS Systemes d'Information, http://csgroup.eu
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
# block_device_unmount.sh
# Unmount a block device and removes the corresponding entry from /etc/fstab

set -u -o pipefail

function print_error {
    read line file <<<$(caller)
    echo "An error occurred in line $line of file $file:" "{"`sed "${line}q;d" "$file"`"}" >&2
}
trap print_error ERR

{{block "list" .Drives}}{{"\n"}}{{range .}}{{println "sudo umount" . "&& sudo pvcreate" . "-f > /dev/null 2>&1"}}{{end}}{{end}}
sudo vgextend {{.Name}}VG {{block "added" .Drives}}{{range .}}{{print . }}{{end}}{{end}}
sudo lvextend -l +100%FREE /dev/mapper/{{.Name}}VG-{{.Name}}
sudo resize2fs  /dev/mapper/{{.Name}}VG-{{.Name}}
