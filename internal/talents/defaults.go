package talents

import "embed"

//go:generate sh -c "rm -rf defaults && mkdir defaults && cp ../../talents/*.md defaults/ && echo '*.md' > defaults/.gitignore"

//go:embed defaults/*.md
var DefaultFiles embed.FS
