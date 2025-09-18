package main

import (
	"bytes"
	"context"
	"flag"
	"log"
	"time"

	"app/cfg"
	"app/db"
	"app/pkg/s3client"

	"os"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

func main() {
	cfgPath := flag.String("cfg-path", "cfg/cfg.yaml", "path to config file")
	cleanup := flag.Bool("cleanup", false, "delete old inline media blobs after migration")
	flag.Parse()

	var c *cfg.Config
	data, err := os.ReadFile(*cfgPath)
	if err != nil {
		log.Fatal("read cfg: ", err)
	}
	if err := yaml.Unmarshal(data, &c); err != nil {
		log.Fatal("unmarshal cfg: ", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	dbObj, err := db.New(ctx, &c.DB)
	if err != nil {
		log.Fatal("db init: ", err)
	}

	s3, err := s3client.New(ctx, &c.S3)
	if err != nil {
		log.Fatal("s3 init: ", err)
	}
	if err := s3.EnsureBucket(ctx, s3client.CharDataBucket); err != nil {
		log.Fatal("ensure bucket: ", err)
	}

	rows, err := dbObj.Query(ctx, `select id, data from char_cards limit 100000`)
	if err != nil {
		log.Fatal("select cards: ", err)
	}
	defer rows.Close()

	uploadedImages := 0
	uploadedVoices := 0
	for rows.Next() {
		var id uuid.UUID
		var data *db.CardData
		if err := rows.Scan(&id, &data); err != nil {
			log.Fatal("scan: ", err)
		}

		// Phase 1: upload and set IDs (do NOT delete inline bytes yet)
		if len(data.Image) > 0 && data.ImageID == "" {
			imageID := uuid.New().String()
			if err := s3.PutObject(ctx, s3client.CharDataBucket, imageID, bytesReader(data.Image), int64(len(data.Image)), "image/png"); err != nil {
				log.Fatal("put image: ", err)
			}
			if _, err := dbObj.Exec(ctx, `update char_cards set data = jsonb_set(data, '{image_id}', to_jsonb($2::text), true), updated_at = now() where id = $1`, id, imageID); err != nil {
				log.Fatal("update image id: ", err)
			}
			uploadedImages++
		}

		if len(data.VoiceReference) > 0 && data.VoiceID == "" {
			voiceID := uuid.New().String()
			if err := s3.PutObject(ctx, s3client.CharDataBucket, voiceID, bytesReader(data.VoiceReference), int64(len(data.VoiceReference)), "application/octet-stream"); err != nil {
				log.Fatal("put voice: ", err)
			}
			if _, err := dbObj.Exec(ctx, `update char_cards set data = jsonb_set(data, '{voice_id}', to_jsonb($2::text), true), updated_at = now() where id = $1`, id, voiceID); err != nil {
				log.Fatal("update voice id: ", err)
			}
			uploadedVoices++
		}
	}
	if err := rows.Err(); err != nil {
		log.Fatal("iter: ", err)
	}

	log.Printf("uploaded: images=%d, voices=%d", uploadedImages, uploadedVoices)

	// Phase 2: optional cleanup of old inline blobs (only when IDs exist)
	if *cleanup {
		if ct, err := dbObj.Exec(ctx, `update char_cards set data = data - 'image' where (data ? 'image') and (data ? 'image_id')`); err != nil {
			log.Fatal("cleanup images: ", err)
		} else {
			log.Printf("cleanup removed inline images from %d rows", ct.RowsAffected())
		}
		if ct, err := dbObj.Exec(ctx, `update char_cards set data = data - 'voice_reference' where (data ? 'voice_reference') and (data ? 'voice_id')`); err != nil {
			log.Fatal("cleanup voices: ", err)
		} else {
			log.Printf("cleanup removed inline voice refs from %d rows", ct.RowsAffected())
		}
	}
}

// bytesReader returns a ReadSeeker for a byte slice without copying
func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }
