// Package carddav implements a CardDAV server backed by the contacts
// store. It bridges the contacts.Store CRUD operations to the CardDAV
// protocol via emersion/go-webdav, enabling native contact apps
// (macOS Contacts.app, iOS, Thunderbird) to sync with Thane's contact
// directory.
package carddav
