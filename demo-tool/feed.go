package main

// itemToMap renders a demoItem as the published item object (without `sig`).
// Fields follow spec/feeds.md §1.1: id, title, content_html, image (+
// image_sha256 when linked), date_published, date_modified, tags, language,
// attachments. Deliberately absent: content_text, summary, url, authors.
func itemToMap(it demoItem) map[string]any {
	m := map[string]any{
		"id":             it.ID,
		"title":          it.Title,
		"content_html":   it.ContentHTML,
		"date_published": it.Published,
	}
	if it.Image != "" {
		// Linked image: image_sha256 is REQUIRED (spec/feeds.md §1.1) — the
		// app verifies the fetched bytes before rendering.
		m["image"] = metadataOrigin + "/media/img/" + it.Image
		m["image_sha256"] = mediaSHA256(it.Image)
	}
	if it.DateModified != "" {
		m["date_modified"] = it.DateModified
	}
	if len(it.Tags) > 0 {
		m["tags"] = it.Tags
	}
	if it.Language != "" {
		m["language"] = it.Language
	}
	if len(it.Attachments) > 0 {
		m["attachments"] = it.Attachments
	}
	return m
}
