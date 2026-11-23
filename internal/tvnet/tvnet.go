// Package tvnet combines the independent, local TV transports.
package tvnet

import (
	"context"
	"errors"

	"simplewebplayer/internal/casting"
	"simplewebplayer/internal/castnet"
	"simplewebplayer/internal/dlnanet"
)

type Transport struct {
	google casting.Transport
	dlna   casting.Transport
}

func New() *Transport { return &Transport{google: castnet.New(), dlna: dlnanet.New()} }

var _ casting.Transport = (*Transport)(nil)

func (t *Transport) Discover(ctx context.Context, lan casting.LAN) ([]casting.Device, error) {
	type found struct {
		protocol string
		devices  []casting.Device
		err      error
	}
	out := make(chan found, 2)
	for _, kind := range []string{casting.ProtocolGoogle, casting.ProtocolDLNA} {
		go func() {
			devices, err := t.backend(kind).Discover(ctx, lan)
			out <- found{kind, devices, err}
		}()
	}
	var google, dlna []casting.Device
	var failures []error
	for range 2 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case r := <-out:
			if r.err != nil {
				failures = append(failures, r.err)
			} else if r.protocol == casting.ProtocolGoogle {
				google = r.devices
			} else {
				dlna = r.devices
			}
		}
	}
	if len(failures) == 2 {
		return nil, errors.Join(failures...)
	}
	// Alternate results so one busy protocol cannot hide all entries for the other
	// when the API caps the number of visible results.
	devices := make([]casting.Device, 0, len(google)+len(dlna))
	for len(google)+len(dlna) > 0 {
		if len(google) > 0 {
			d := google[0]
			d.Protocol = casting.ProtocolGoogle
			devices = append(devices, d)
			google = google[1:]
		}
		if len(dlna) > 0 {
			d := dlna[0]
			d.Protocol = casting.ProtocolDLNA
			devices = append(devices, d)
			dlna = dlna[1:]
		}
	}
	return devices, nil
}

func (t *Transport) backend(kind string) casting.Transport {
	switch kind {
	case casting.ProtocolGoogle:
		return t.google
	case casting.ProtocolDLNA:
		return t.dlna
	default:
		return nil
	}
}

func (t *Transport) Load(ctx context.Context, lan casting.LAN, device casting.Device, media casting.Media) error {
	if tr := t.backend(device.Kind()); tr != nil && device.AllowedOn(lan) {
		return tr.Load(ctx, lan, device, media)
	}
	return errors.New("TV transport: invalid local device")
}

func (t *Transport) Stop(ctx context.Context, lan casting.LAN, device casting.Device, uri string) error {
	if tr := t.backend(device.Kind()); tr != nil && device.AllowedOn(lan) {
		return tr.Stop(ctx, lan, device, uri)
	}
	return errors.New("TV transport: invalid local device")
}
