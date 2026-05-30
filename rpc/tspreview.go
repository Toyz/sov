package rpc

import "github.com/Toyz/sov/rpc/tsrender"

// TSPreviewForMethod returns one-line TypeScript previews of the params
// type and the result type for an RPC method. Thin shim over
// tsrender.RenderInline — the shared renderer is the source of truth
// for both this preview path and the sovgen CLI's full `.d.ts`
// emission. Output unchanged from the pre-shim implementation.
func TSPreviewForMethod(entry *methodEntry) (request, response string) {
	if entry.hasParams {
		request = tsrender.RenderInline(entry.paramType)
	} else {
		request = "void"
	}
	if entry.resultType != nil {
		response = tsrender.RenderInline(entry.resultType)
	} else {
		response = "void"
	}
	return
}
