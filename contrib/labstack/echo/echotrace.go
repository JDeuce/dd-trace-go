// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016 Datadog, Inc.

// Package echo provides functions to trace the labstack/echo package (https://github.com/labstack/echo).
package echo

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"

	"gopkg.in/DataDog/dd-trace-go.v1/contrib/internal/httptrace"
	"gopkg.in/DataDog/dd-trace-go.v1/ddtrace"
	"gopkg.in/DataDog/dd-trace-go.v1/ddtrace/ext"
	"gopkg.in/DataDog/dd-trace-go.v1/ddtrace/tracer"
	"gopkg.in/DataDog/dd-trace-go.v1/internal/log"

	"github.com/labstack/echo"
)

// Middleware returns echo middleware which will trace incoming requests.
func Middleware(opts ...Option) echo.MiddlewareFunc {
	cfg := new(config)
	defaults(cfg)
	for _, fn := range opts {
		fn(cfg)
	}
	log.Debug("contrib/labstack/echo: Configuring Middleware: %#v", cfg)
	spanOpts := []ddtrace.StartSpanOption{
		tracer.ServiceName(cfg.serviceName),
		tracer.Tag(ext.Component, "labstack/echo"),
		tracer.Tag(ext.SpanKind, ext.SpanKindServer),
	}
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			request := c.Request()
			resource := request.Method + " " + c.Path()
			opts := append(spanOpts, tracer.ResourceName(resource))

			if !math.IsNaN(cfg.analyticsRate) {
				opts = append(opts, tracer.Tag(ext.EventSampleRate, cfg.analyticsRate))
			}
			opts = append(opts, ddhttptrace.HeaderTagsFromRequest(request, cfg.headersAsTags))

			var finishOpts []tracer.FinishOption
			if cfg.noDebugStack {
				finishOpts = []tracer.FinishOption{tracer.NoDebugStack()}
			}

			span, ctx := httptrace.StartRequestSpan(request, opts...)
			defer func() {
				//httptrace.FinishRequestSpan(span, c.Response().Status, finishOpts...)
				span.Finish(finishOpts...)
			}()

			// pass the span through the request context
			c.SetRequest(request.WithContext(ctx))

			// serve the request to the next middleware
			err := next(c)
			if err != nil {
				// invokes the registered HTTP error handler
				c.Error(err)

				// It is impossible to determine what the final status code of a request is in echo.
				// This is the best we can do.
				var echoErr *echo.HTTPError
				if errors.As(err, &echoErr) {
					if cfg.isStatusError(echoErr.Code) {
						finishOpts = append(finishOpts, tracer.WithError(err))
					}
					span.SetTag(ext.HTTPCode, strconv.Itoa(echoErr.Code))
				} else {
					// Any error that is not an *echo.HTTPError will be treated as an error with 500 status code.
					if cfg.isStatusError(500) {
						finishOpts = append(finishOpts, tracer.WithError(err))
					}
					span.SetTag(ext.HTTPCode, "500")
				}
			} else if status := c.Response().Status; status > 0 {
				if cfg.isStatusError(status) {
					finishOpts = append(finishOpts, tracer.WithError(fmt.Errorf("%d: %s", status, http.StatusText(status))))
				}
				span.SetTag(ext.HTTPCode, strconv.Itoa(status))
			} else {
				if cfg.isStatusError(200) {
					finishOpts = append(finishOpts, tracer.WithError(fmt.Errorf("%d: %s", 200, http.StatusText(200))))
				}
				span.SetTag(ext.HTTPCode, "200")
			}
			return err
		}
	}
}
