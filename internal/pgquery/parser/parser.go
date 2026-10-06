//go:build !pgquery_cgo && !tinygo

package parser

import "github.com/OriginalDaniel02/dbguard/internal/pgquery/internal/pgerror"

type Error = pgerror.Error
