// Package adbtest provides integration testing utilities for Android WebView
// applications. It drives an owned emulator or an explicitly selected attached
// device, interacting with both native UI (permission dialogs, file pickers,
// system chrome) and WebView content (DOM elements, JS evaluation) using
// semantic text/selector matching.
//
// Use [Setup] in TestMain to start an emulator or attach to an explicit device,
// install an APK, and get a [Device] handle. Then use device.UI for native
// interactions and device.CDP for WebView interactions.
package adbtest
