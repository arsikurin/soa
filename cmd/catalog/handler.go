package main

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"marketplace/internal/api"
)

type healthServer struct{}

var _ api.StrictServerInterface = healthServer{}

func (healthServer) GetHealth(context.Context, api.GetHealthRequestObject) (api.GetHealthResponseObject, error) {
	return api.GetHealth200JSONResponse{Status: api.Ok}, nil
}

func newHandler() http.Handler {
	router := chi.NewRouter()
	// Keep HEAD support; net/http omits the response body on the wire.
	router.Use(middleware.GetHead)
	return api.HandlerFromMux(api.NewStrictHandler(healthServer{}, nil), router)
}
