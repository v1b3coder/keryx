package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/v1b3coder/keryx/sdk/join"
)

func (a *app) joinCmd() *cobra.Command {
	var channels, privateFeeds []string
	var origin string
	cmd := &cobra.Command{
		Use:   "join-url",
		Short: "Build the join URL (no QR)",
		RunE: func(_ *cobra.Command, _ []string) error {
			if origin == "" {
				o, err := a.publisher().JoinOrigin(a.ctx())
				if err != nil {
					return err
				}
				origin = o
			}
			payload, err := join.BuildPayload(channels, privateFeeds)
			if err != nil {
				return err
			}
			url, err := join.JoinURL(origin, payload)
			if err != nil {
				return err
			}
			if a.jsonOut {
				a.render(map[string]any{"join_url": url, "channels": channels, "private_feeds": privateFeeds}, "")
			} else {
				fmt.Println(url)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&origin, "origin", "", "join origin (default: repo base origin)")
	cmd.Flags().StringSliceVar(&channels, "channels", nil, "suggested channels")
	cmd.Flags().StringSliceVar(&privateFeeds, "private-feed", nil, "private capability feed URLs")
	return cmd
}

func (a *app) qrCmd() *cobra.Command {
	var channels, privateFeeds []string
	var origin, out string
	var size int
	cmd := &cobra.Command{
		Use:   "qr",
		Short: "Render the join URL as a PNG QR code",
		RunE: func(_ *cobra.Command, _ []string) error {
			if out == "" {
				return fmt.Errorf("--out is required")
			}
			if origin == "" {
				o, err := a.publisher().JoinOrigin(a.ctx())
				if err != nil {
					return err
				}
				origin = o
			}
			payload, err := join.BuildPayload(channels, privateFeeds)
			if err != nil {
				return err
			}
			url, err := join.JoinURL(origin, payload)
			if err != nil {
				return err
			}
			png, err := join.QR(url, size)
			if err != nil {
				return err
			}
			if err := os.WriteFile(out, png, 0o644); err != nil {
				return err
			}
			a.render(map[string]any{"out": out, "join_url": url, "bytes": len(png)},
				fmt.Sprintf("wrote %s\njoin URL: %s", out, url))
			return nil
		},
	}
	cmd.Flags().StringVar(&origin, "origin", "", "join origin (default: repo base origin)")
	cmd.Flags().StringSliceVar(&channels, "channels", nil, "suggested channels")
	cmd.Flags().StringSliceVar(&privateFeeds, "private-feed", nil, "private capability feed URLs")
	cmd.Flags().StringVar(&out, "out", "qr.png", "output PNG")
	cmd.Flags().IntVar(&size, "size", 512, "PNG pixel size")
	return cmd
}
