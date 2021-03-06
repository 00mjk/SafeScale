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

package listeners

import (
	"context"

	"github.com/CS-SI/SafeScale/lib/utils/debug"

	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/CS-SI/SafeScale/lib"
	"github.com/CS-SI/SafeScale/lib/server/handlers"
	srvutils "github.com/CS-SI/SafeScale/lib/server/utils"
	"github.com/CS-SI/SafeScale/lib/utils/fail"
)

// TemplateHandler exists to ease integration tests
var TemplateHandler = handlers.NewTemplateHandler

// safescale template list --all=false

// TemplateListener host service server grpc
type TemplateListener struct{}

// List available templates
func (s *TemplateListener) List(ctx context.Context, in *pb.TemplateListRequest) (tl *pb.TemplateList, err error) {
	if s == nil {
		return nil, status.Errorf(codes.FailedPrecondition, fail.InvalidInstanceError().Message())
	}
	all := in.GetAll()

	tracer := debug.NewTracer(nil, "", true).WithStopwatch().GoingIn()
	defer tracer.OnExitTrace()()
	defer fail.OnExitLogError(tracer.TraceMessage(""), &err)()

	ctx, cancelFunc := context.WithCancel(ctx)
	if err := srvutils.JobRegister(ctx, cancelFunc, "Teplates List"); err == nil {
		defer srvutils.JobDeregister(ctx)
	}

	tenant := GetCurrentTenant()
	if tenant == nil {
		log.Info("Can't list templates: no tenant set")
		return nil, status.Errorf(codes.FailedPrecondition, "cannot list templates: no tenant set")
	}

	handler := TemplateHandler(tenant.Service)
	templates, err := handler.List(ctx, all)
	if err != nil {
		return nil, status.Errorf(codes.Internal, getUserMessage(err))
	}

	// Map abstract.Host to pb.Host
	var pbTemplates []*pb.HostTemplate
	for _, template := range templates {
		pbt, err := srvutils.ToPBHostTemplate(&template)
		if err != nil {
			log.Warn(err)
			continue
		}
		pbTemplates = append(pbTemplates, pbt)
	}
	rv := &pb.TemplateList{Templates: pbTemplates}
	return rv, nil
}
