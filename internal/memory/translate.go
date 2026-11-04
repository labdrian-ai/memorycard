package memory

import (
	"sort"
	"strings"
)

// Spanish questions over English notes share few words, so the lexical
// search missed them. A query that reads as Spanish (see readsSpanish) gets,
// after each Spanish word, the English words a note would use for it. The
// table maps accent-folded Spanish stems, matched as word prefixes (so plurals
// and verb forms are covered), to English words. Some stems are English words
// or prefixes of them, which is why English and bare-keyword queries are never
// looked up.
var esStems = map[string][]string{
	// change and repair
	"arregl": {"fix", "fixed"}, "correg": {"fix", "fixed"}, "corrig": {"fix", "fixed"},
	"cambi": {"change", "changed"}, "agreg": {"add", "added"}, "anadir": {"add"}, "anadimos": {"add", "added"}, "anadido": {"added"},
	"quitar": {"remove"}, "quitam": {"remove", "removed"}, "quitad": {"removed"},
	"elimin": {"delete", "remove"}, "borrar": {"delete"}, "borramos": {"delete", "deleted"}, "borrado": {"deleted"},
	"reemplaz": {"replace", "replaced"}, "renombr": {"rename", "renamed"},
	"actualiz": {"update", "updated"}, "movim": {"move", "moved"}, "mover": {"move"},
	"migracion": {"migration"}, "migrar": {"migrate"}, "migram": {"migrate", "migrated"},
	"desplieg": {"deploy", "deployment"}, "despleg": {"deploy", "deployed"},
	"instalar": {"install"}, "instalacion": {"install", "installation"}, "instalad": {"installed"},
	"instalam": {"install", "installed"}, "compilar": {"build", "compile"}, "compilacion": {"build"},
	"construcc": {"build"}, "subir": {"push", "upload"}, "subimos": {"push", "upload"},
	"reinici": {"restart", "reset"}, "arranc": {"start", "startup"}, "reintent": {"retry"},
	"respald": {"backup"}, "copia": {"copy", "backup"}, "restaur": {"restore"},
	"guardar": {"save"}, "guardam": {"save", "saved"}, "guardad": {"saved"},
	"escrib": {"write"}, "escritura": {"write"}, "lectura": {"read"}, "leer": {"read"},
	"usamos": {"use", "used"}, "usar": {"use"}, "decidim": {"decided", "decision"},
	"elegim": {"chose", "choice"}, "eleccion": {"choice"},
	// failure
	"fallo": {"fail", "failed", "failure"}, "falla": {"fail", "failed", "failure"},
	"fallaron": {"failed"}, "caida": {"crash", "outage"}, "rompio": {"broke", "broken"}, "rompimos": {"broke"}, "roto": {"broken"},
	"advertencia": {"warning"}, "aviso": {"warning", "notice"},
	// data and storage
	"datos": {"data", "database"}, "busqueda": {"search"}, "buscar": {"search"}, "buscam": {"search"},
	"memoria": {"memory"}, "recuerd": {"memory", "recall"}, "esquema": {"schema"},
	"tabla": {"table"}, "indice": {"index"}, "columna": {"column"}, "fila": {"row"},
	"registro": {"record", "log"}, "bitacora": {"log", "logbook"}, "consulta": {"query"},
	"resultado": {"result"}, "archivo": {"file"}, "archivar": {"archive"}, "archivad": {"archived"},
	"carpeta": {"folder", "directory"}, "almacen": {"store", "storage"},
	"incrustacion": {"embedding"}, "fragmento": {"chunk", "fragment"}, "trozo": {"chunk"},
	"resumen": {"summary"}, "contexto": {"context"}, "densa": {"dense"}, "denso": {"dense"},
	"hibrid": {"hybrid"}, "obsolet": {"outdated", "stale"}, "desactualizad": {"outdated", "stale"},
	"vigente": {"current"}, "antigu": {"old"}, "nuevo": {"new"}, "nueva": {"new"}, "viejo": {"old"}, "vieja": {"old"},
	// software
	"configuraci": {"configuration", "config"}, "configurar": {"configure", "config"},
	"configuram": {"configured", "config"}, "configurad": {"configured", "config"},
	"herramienta": {"tool"}, "habilidad": {"skill"}, "agente": {"agent"}, "modelo": {"model"},
	"proveedor": {"provider"}, "servicio": {"service"}, "servidor": {"server"},
	"usuario": {"user"}, "cliente": {"client", "customer"}, "conexion": {"connection"},
	"puerto": {"port"}, "seguridad": {"security"}, "permiso": {"permission"},
	"autenticacion": {"authentication", "auth"}, "autorizacion": {"authorization", "auth"},
	"contrasena": {"password"}, "clave": {"key"}, "sesion": {"session"}, "firma": {"signature", "signing"},
	"cifrad": {"encryption", "encrypted"}, "encriptac": {"encryption"},
	"prueba": {"test", "tests"}, "testeo": {"test", "testing"}, "rama": {"branch"},
	"repositorio": {"repository", "repo"}, "codigo": {"code"}, "funcion": {"function"},
	"metodo": {"method"}, "clase": {"class"}, "arquitectura": {"architecture"},
	"diseno": {"design"}, "capa": {"layer"}, "dominio": {"domain"}, "adaptador": {"adapter"},
	"pantalla": {"screen"}, "boton": {"button"}, "formulario": {"form"}, "pagina": {"page"},
	"ventana": {"window"}, "comando": {"command"}, "bandera": {"flag"}, "opcion": {"option"},
	"ajuste": {"setting", "settings"}, "entorno": {"environment"}, "dependencia": {"dependency"},
	"paquete": {"package"}, "libreria": {"library"}, "biblioteca": {"library"},
	"complemento": {"plugin"}, "gancho": {"hook"}, "disparador": {"trigger"},
	"flujo": {"flow", "workflow"}, "revision": {"review"}, "revisar": {"review"}, "revisam": {"review", "reviewed"},
	"tarea": {"task"}, "requisito": {"requirement"}, "requerimiento": {"requirement"},
	"especificacion": {"spec", "specification"}, "regla": {"rule"}, "convencion": {"convention"},
	"patron": {"pattern"}, "plantilla": {"template"}, "documentacion": {"documentation", "docs"},
	"documento": {"document"}, "etiqueta": {"label", "tag"}, "titulo": {"title"},
	"cuerpo": {"body"}, "tipo": {"type"}, "alcance": {"scope"}, "defecto": {"default"},
	"predetermin": {"default"}, "proyecto": {"project"}, "equipo": {"team"}, "dueno": {"owner"},
	"nube": {"cloud"}, "correo": {"email", "mail"}, "reunion": {"meeting"},
	"bloqueo": {"lock"}, "candado": {"lock"}, "concurrencia": {"concurrency"}, "hilo": {"thread"},
	"proceso": {"process"}, "cola": {"queue"}, "espera": {"wait", "timeout"},
	// measurement
	"rendimiento": {"performance"}, "velocidad": {"speed"}, "lento": {"slow"}, "lenta": {"slow"}, "rapido": {"fast"}, "rapida": {"fast"},
	"costo": {"cost"}, "coste": {"cost"}, "tamano": {"size"}, "limite": {"limit"},
	"tiempo": {"time"}, "medicion": {"measurement"}, "medir": {"measure"}, "medimos": {"measured"},
	"evaluacion": {"evaluation", "benchmark"}, "exactitud": {"accuracy"}, "precision": {"precision", "accuracy"},
	"puntaje": {"score"}, "puntuacion": {"score"}, "umbral": {"threshold"},
	"pregunta": {"question"}, "respuesta": {"answer", "response"}, "idioma": {"language"},
	"espanol": {"spanish"}, "ingles": {"english"}, "traduc": {"translation", "translate"},
	"palabra": {"word"},
	// trading and commerce
	"vela": {"candle"}, "operacion": {"trade", "operation"}, "entrada": {"entry", "input"},
	"salida": {"exit", "output"}, "estrategia": {"strategy"}, "riesgo": {"risk"},
	"ganancia": {"profit"}, "perdida": {"loss"}, "mercado": {"market"}, "historic": {"historical"},
	"grafico": {"chart"}, "precio": {"price", "pricing"}, "pago": {"payment"},
	"factura": {"invoice"}, "pedido": {"order"}, "producto": {"product"}, "inventario": {"inventory"},
	"mayorista": {"wholesale"},
}

// esStemList holds the table's stems longest first, so the most specific
// stem wins ("configuraci" before a shorter one).
var esStemList = func() []string {
	out := make([]string, 0, len(esStems))
	for s := range esStems {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i]) != len(out[j]) {
			return len(out[i]) > len(out[j])
		}
		return out[i] < out[j]
	})
	return out
}()

// englishFunctionWords and spanishFunctionWords decide whether a query reads
// as Spanish; only then are its words looked up.
var englishFunctionWords = setOf(`the of and to in for is was were did does we our what why how which with on it that this from`)
var spanishFunctionWords = setOf(`el la los las de del que en por para con un una se lo como cual cuando donde porque y sin sobre es fue al mi nuestro nuestra`)

func setOf(words string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(words) {
		m[w] = true
	}
	return m
}

// spanishMarks are letters and signs English text does not use.
const spanishMarks = "áéíóúüñ¿¡"

// readsSpanish reports whether a query reads as Spanish: it has more Spanish
// than English function words or, on a tie (a keyword query has neither), it
// uses Spanish spelling: an accent, ñ, ¿ or ¡. Bare keywords without such
// marks stay untranslated, because some stems are English words or prefixes
// of them ("patron", "precision", "revision", "precious", "indices").
// lowered is the lowercased query; words are its accent-folded words.
func readsSpanish(lowered string, words []string) bool {
	en, es := 0, 0
	for _, w := range words {
		if englishFunctionWords[w] {
			en++
		}
		if spanishFunctionWords[w] {
			es++
		}
	}
	if en != es {
		return es > en
	}
	return strings.ContainsAny(lowered, spanishMarks)
}

// english returns the English words for an accent-folded Spanish word, or nil.
// A stem of four letters or fewer ("cola", "capa") must be the whole word or
// its plural, since as a prefix it would catch unrelated words ("colaborar",
// "capacidad").
func english(w string) []string {
	for _, stem := range esStemList {
		if len(stem) <= 4 {
			if w == stem || w == stem+"s" || w == stem+"es" {
				return esStems[stem]
			}
			continue
		}
		if strings.HasPrefix(w, stem) {
			return esStems[stem]
		}
	}
	return nil
}
