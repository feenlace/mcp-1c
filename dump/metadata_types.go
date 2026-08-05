package dump

// MetadataType describes a single 1C metadata kind with its English singular
// name (used in tool input), English plural (the dump directory name), and
// the Russian singular display name.
type MetadataType struct {
	SingularEng string // e.g. "Catalog"
	PluralEng   string // e.g. "Catalogs"   (dump directory)
	RussianName string // e.g. "Справочник"  (display prefix)
}

// metadataTypes is the single source of truth for all 1C metadata type
// mappings. Both objectTypeToDumpDir and dumpDirNames are derived from it.
var metadataTypes = []MetadataType{
	{"Catalog", "Catalogs", "Справочник"},
	{"Document", "Documents", "Документ"},
	{"DataProcessor", "DataProcessors", "Обработка"},
	{"Report", "Reports", "Отчет"},
	{"InformationRegister", "InformationRegisters", "РегистрСведений"},
	{"AccumulationRegister", "AccumulationRegisters", "РегистрНакопления"},
	{"AccountingRegister", "AccountingRegisters", "РегистрБухгалтерии"},
	{"CalculationRegister", "CalculationRegisters", "РегистрРасчета"},
	{"ChartOfAccounts", "ChartsOfAccounts", "ПланСчетов"},
	{"ChartOfCharacteristicTypes", "ChartsOfCharacteristicTypes", "ПланВидовХарактеристик"},
	{"ChartOfCalculationTypes", "ChartsOfCalculationTypes", "ПланВидовРасчета"},
	{"ExchangePlan", "ExchangePlans", "ПланОбмена"},
	{"BusinessProcess", "BusinessProcesses", "БизнесПроцесс"},
	{"Task", "Tasks", "Задача"},
	{"Enum", "Enums", "Перечисление"},
	{"Constant", "Constants", "Константа"},
}

// objectTypeToDumpDir maps singular English type name to plural English dump
// directory name (e.g. "Catalog" -> "Catalogs"). Derived from metadataTypes.
var objectTypeToDumpDir map[string]string

// categoryNames is the SET of strings this package ever puts in the category slot
// of a key: every Russian display name in dumpDirNames, plus the configuration's
// own configModulePrefix.
//
// It exists because "ext" alone cannot tell a namespaced key from an ordinary one.
// A dump root may hold a directory literally named «ext» (the customer whose tree
// started all of this had one), an unknown top-level directory becomes the category
// slot verbatim, and such a key really can reach five segments. What settles it is
// the segment AFTER the extension name: only a category this package emits can
// stand there. See splitModuleKey.
//
// DERIVED, never typed. A kind added to metadataTypes joins this set on the same
// line it joins dumpDirNames, so a new kind cannot silently stop being recognised
// after an "ext." prefix. It is populated at the END of init below, after every
// entry has been added, which is why it is not built anywhere else in the package:
// init order across files would decide whether it was complete.
var categoryNames map[string]struct{}

// dumpDirNames maps plural English dump directory name to Russian display
// name (e.g. "Catalogs" -> "Справочник"). Derived from metadataTypes.
//
// CommonModules is added separately because it has no singular form used in
// tool input (there is no "CommonModule" object type).
var dumpDirNames map[string]string

func init() {
	objectTypeToDumpDir = make(map[string]string, len(metadataTypes))
	dumpDirNames = make(map[string]string, len(metadataTypes)+1)

	for _, mt := range metadataTypes {
		objectTypeToDumpDir[mt.SingularEng] = mt.PluralEng
		dumpDirNames[mt.PluralEng] = mt.RussianName
	}

	// CommonModules has no corresponding singular object type. It only
	// appears as a dump directory, so we add it to dumpDirNames directly.
	dumpDirNames["CommonModules"] = "ОбщийМодуль"

	// CommonForms / CommonCommands are Common-typed metadata: like
	// CommonModules they have no singular object type used in tool input, so
	// they are added to dumpDirNames directly. Without them the indexer emitted
	// raw-English-prefix keys (e.g. "CommonForms.X.МодульФормы") that no
	// resolver ever queries. On-disk these subtrees never contain a plural
	// "Forms"/"Commands" segment, so the keys stay FLAT (no .Форма./.Команда.
	// infix): "ОбщаяФорма.X.МодульФормы", "ОбщаяКоманда.X.МодульКоманды".
	dumpDirNames["CommonForms"] = "ОбщаяФорма"
	dumpDirNames["CommonCommands"] = "ОбщаяКоманда"

	// DocumentJournals: same missing-prefix root cause. The Russian singular
	// "ЖурналДокументов" is the canonical NameRu for DocumentJournal (verified
	// against the metadata type table). Kept on its own line so it can be
	// dropped trivially if a future dump shape ever proves otherwise.
	dumpDirNames["DocumentJournals"] = "ЖурналДокументов"

	// Five service kinds with the same missing-prefix root cause. Each was
	// enumerated on a real demo Бухгалтерия dump (13243 .bsl) as a top-level
	// directory holding modules while having no dumpDirNames entry, so the
	// indexer emitted raw-English-prefix keys such as
	// "SettingsStorages.НастройкиНовостей.МодульМенеджера".
	//
	// Every Russian singular below is taken verbatim from serviceKindEnToRu in
	// subsystem_kinds.go, keyed by the English SINGULAR (the dump directory is
	// plural). They are added here rather than to metadataTypes because that
	// slice also feeds objectTypeToDumpDir, the tool-input type -> form-directory
	// map used by formparser; these kinds are not form-bearing tool inputs and
	// must not silently appear there.
	//
	// One line each, so any single kind can be dropped if a future dump shape
	// ever contradicts it.
	dumpDirNames["HTTPServices"] = "HTTPСервис"            // 5 modules measured
	dumpDirNames["WebServices"] = "WebСервис"              // 16 modules measured
	dumpDirNames["SettingsStorages"] = "ХранилищеНастроек" // 22 modules measured
	dumpDirNames["FilterCriteria"] = "КритерийОтбора"      // 3 modules measured
	dumpDirNames["Sequences"] = "Последовательность"       // 5 modules measured

	// The eighteen kinds a real configuration DECLARES that the table still did
	// not know. Measured: the Configuration.xml of dumps/dump_2 lists 41 distinct
	// kinds in <ChildObjects>, the table held 25, and all eighteen directories
	// below exist on disk in that same dump.
	//
	// EVERY ONE OF THEM HOLDS ZERO .bsl FILES, and saying so is the point rather
	// than an aside. That is why no module census could ever have found them: a
	// role, a style, a subsystem, a language, a picture and a defined type have no
	// module by construction, so the fixture behind guard 1 in
	// module_key_guard_test.go — which records directories caught holding modules —
	// is structurally blind to them.
	//
	// They are added because dumpDirNames is no longer only a display table. It is
	// what dumpRootMarker reads to decide whether a path segment can be the top of a
	// dump, so a kind missing here is a shape that cannot be recognised as a dump
	// root and cannot be anchored past a wrapper. The entry earns its place through
	// recognition, not through any key it changes.
	//
	// NO RUSSIAN NAME HERE WAS WRITTEN FROM KNOWLEDGE. Each is the string this
	// package already uses for that kind in serviceKindEnToRu (subsystem_kinds.go),
	// which is the table that canonicalises subsystem membership against the live
	// platform full name. TestDumpDirRussianNamesMatchTheKindTables asserts that
	// correspondence for all 41 kinds, so a name typed here rather than copied
	// fails the build.
	dumpDirNames["CommandGroups"] = "ГруппаКоманд"
	dumpDirNames["CommonAttributes"] = "ОбщийРеквизит"
	dumpDirNames["CommonPictures"] = "ОбщаяКартинка"
	dumpDirNames["CommonTemplates"] = "ОбщийМакет"
	dumpDirNames["DefinedTypes"] = "ОпределяемыйТип"
	dumpDirNames["DocumentNumerators"] = "НумераторДокументов"
	dumpDirNames["EventSubscriptions"] = "ПодпискаНаСобытие"
	dumpDirNames["FunctionalOptions"] = "ФункциональнаяОпция"
	dumpDirNames["FunctionalOptionsParameters"] = "ПараметрФункциональныхОпций"
	dumpDirNames["Languages"] = "Язык"
	dumpDirNames["Roles"] = "Роль"
	dumpDirNames["ScheduledJobs"] = "РегламентноеЗадание"
	dumpDirNames["SessionParameters"] = "ПараметрСеанса"
	dumpDirNames["StyleItems"] = "ЭлементСтиля"
	dumpDirNames["Styles"] = "Стиль"
	dumpDirNames["Subsystems"] = "Подсистема"
	dumpDirNames["WSReferences"] = "WSСсылка"
	dumpDirNames["XDTOPackages"] = "ПакетXDTO"

	// Bots stands apart, and the difference is stated rather than hidden.
	//
	// It is a genuine configuration child class — a real EDT manifest carries
	// <bots>Bot.ОфисМенеджер</bots> beside a live src/Bots directory — but it is
	// absent from the ERP manifest measured above, so it is not in the fixture, and
	// it is the ONE kind here whose Russian singular is not copied from another
	// table: no subsystem table lists a bot.
	//
	// The plural collection «Боты» IS known to this repository, in
	// testdata/config_metadata_properties.txt, the snapshot of the platform type
	// ОбъектМетаданныхКонфигурация that configModuleNames also draws its four names
	// from. The singular below is that plural reduced by the same rule
	// subsystem_kinds.go already applies and documents (ЭлементыСтиля gives
	// ЭлементСтиля, ВнешниеИсточникиДанных gives ВнешнийИсточникДанных).
	//
	// So it is derived, not read, and TestBotsIsTheOneDerivedRussianName says so in
	// the tree: if the platform full name ever turns out to spell it otherwise,
	// that test is the single place to correct.
	//
	// ExternalDataProcessors is deliberately NOT here. It is not a configuration
	// child class at all but a standalone root mdclass, and its projects carry no
	// Configuration.mdo; admitting it would make dumpRootMarker accept a directory
	// that can never be the top of a configuration dump.
	dumpDirNames["Bots"] = "Бот"

	// LAST, and the position is the point: every entry above has to be in place
	// before the set is taken, and a table completed after this line would leave
	// its kinds unrecognised behind an "ext." prefix.
	categoryNames = make(map[string]struct{}, len(dumpDirNames)+1)
	for _, ru := range dumpDirNames {
		categoryNames[ru] = struct{}{}
	}
	categoryNames[configModulePrefix] = struct{}{}
}
