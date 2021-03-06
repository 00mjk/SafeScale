#!/usr/bin/env bash -x
#
# Copyright 2018-2020, CS Systemes d'Information, http://csgroup.eu
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

#### Installs and configure common tools for any kind of nodes ####

install_common_requirements() {
    echo "Installing common requirements..."

    export LANG=C

    # Disable SELinux
    setenforce 0
    sed -i 's/^SELINUX=.*$/SELINUX=disabled/g' /etc/selinux/config

    # Upgrade to last CentOS revision
    rm -rf /usr/lib/python2.7/site-packages/backports.ssl_match_hostname-3.5.0.1-py2.7.egg-info && \
    sfRetry 3m 5 "sfYum install -y python-backports-ssl_match_hostname" && \
    sfRetry 3m 5 "sfYum upgrade --assumeyes --tolerant" && \
    sfRetry 3m 5 "sfYum update --assumeyes"
    [ $? -ne 0 ] && exit 192

    # Create group nogroup
    groupadd nogroup &>/dev/null

    # Creates user {{.ClusterAdminUsername}}
    useradd -s /bin/bash -m -d /home/{{.ClusterAdminUsername}} {{.ClusterAdminUsername}}
    groupadd -r -f docker &>/dev/null
    usermod -aG docker {{.ClusterAdminUsername}}
    echo -e "{{ .ClusterAdminPassword }}\n{{ .ClusterAdminPassword }}" | passwd {{.ClusterAdminUsername}}
    mkdir -p ~{{.ClusterAdminUsername}}/.ssh && chmod 0700 ~{{.ClusterAdminUsername}}/.ssh
    echo "{{ .SSHPublicKey }}" >~{{.ClusterAdminUsername}}/.ssh/authorized_keys
    echo "{{ .SSHPrivateKey }}" >~{{.ClusterAdminUsername}}/.ssh/id_rsa
    chmod 0400 ~{{.ClusterAdminUsername}}/.ssh/*
    echo "{{.ClusterAdminUsername}} ALL=(ALL) NOPASSWD:ALL" >>/etc/sudoers.d/10-admins
    chmod o-rwx /etc/sudoers.d/10-admins

    mkdir -p ~{{.ClusterAdminUsername}}/.local/bin && find ~{{.ClusterAdminUsername}}/.local -exec chmod 0770 {} \;
    cat >>~{{.ClusterAdminUsername}}/.bashrc <<-'EOF'
        pathremove() {
            local IFS=':'
            local NEWPATH
            local DIR
            local PATHVARIABLE=${2:-PATH}
            for DIR in ${!PATHVARIABLE} ; do
                [ "$DIR" != "$1" ] && NEWPATH=${NEWPATH:+$NEWPATH:}$DIR
            done
            export $PATHVARIABLE="$NEWPATH"
        }
        pathprepend() {
            pathremove $1 $2
            local PATHVARIABLE=${2:-PATH}
            export $PATHVARIABLE="$1${!PATHVARIABLE:+:${!PATHVARIABLE}}"
        }
        pathappend() {
            pathremove $1 $2
            local PATHVARIABLE=${2:-PATH}
            export $PATHVARIABLE="${!PATHVARIABLE:+${!PATHVARIABLE}:}$1"
        }
        pathprepend $HOME/.local/bin
        pathappend /opt/mesosphere/bin
EOF
    chown -R {{.ClusterAdminUsername}}:{{.ClusterAdminUsername}} ~{{.ClusterAdminUsername}}

    for i in ~{{.ClusterAdminUsername}}/.hushlogin ~{{.ClusterAdminUsername}}/.cloud-warnings.skip; do
        touch $i
        chown root:{{.ClusterAdminUsername}} $i
        chmod ug+r-wx,o-rwx $i
    done

    # Disables installation of docker-python from yum and adds some requirements
    sfRetry 3m 5 "sfYum remove -y python-docker-py &>/dev/null"
    sfRetry 3m 5 "sfYum install -y yum-versionlock yum-utils tar xz curl wget unzip ipset pigz bind-utils jq rclone" && \
    sfYum versionlock exclude python-docker-py || exit 193

    # Installs PIP
    sfRetry 3m 5 "yum install -y epel-release" || exit 194
    sfRetry 3m 5 "yum makecache" || exit 194
    sfRetry 3m 5 "sfYum install -y python-pip" || sfRetry 3m 5 "sfYum install -y python2-pip" || exit 194

    # Installs docker-python with pip
    pip install -q docker-py==1.10.6 || exit 195

    # Enable overlay module
    echo overlay >/etc/modules-load.d/10-overlay.conf

    # Loads overlay module
    modprobe overlay

    # Mesos needs a subversion release > 1.8
    cat >/etc/yum.repos.d/wandisco-svn.repo <<-'EOF'
[WANdiscoSVN]
name=WANdisco SVN Repo 1.9
enabled=1
baseurl=http://opensource.wandisco.com/centos/7/svn-1.9/RPMS/$basearch/
gpgcheck=1
gpgkey=http://opensource.wandisco.com/RPM-GPG-KEY-WANdisco
EOF
    sfRetry 3m 5 "sfYum install -y subversion"

    echo "Common requirements successfully installed."
}
export -f install_common_requirements

sfRetry 3m 5 "yum makecache"
sfRetry 3m 5 "sfYum install -y time"
/usr/bin/time -p bash -c install_common_requirements
