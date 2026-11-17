package memory

import "strings"

// stopwords are general English words that never say which memory a prompt
// is about. In a small scope almost any word is rare enough to look
// distinctive, so ordinary words ("current", "big", "based") must be listed
// here rather than left to the rarity score.
//
// It is deliberately not a top-N frequency list: frequent words such as
// group, chat, message, state, test, model, number, call, branch, main and
// ever name real things in the projects this store serves. Only add a word
// if it would be noise in any project.
var stopwords = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`
		a an the and or but nor so if then than else as at by for from in into onto of off on to up down
		via per with within without about above below under over across around along among between through
		thru during before after since until till upon toward towards against beyond near behind

		i me my mine myself we us our ours ourselves you your yours yourself he him his she her hers it its
		itself they them their theirs themselves this that these those who whom whose which what whatever
		whoever someone somebody anyone anybody everyone everybody nobody none

		is am are was were be been being do does did done doing have has had having can could will would
		shall should may might must ought get gets got gotten getting

		not no yes yeah yep nope ok okay sure fine thanks thank please pls hi hey hello

		all any both each every either neither few many much more most less least lot lots several some
		enough other another such same own only just also too very really quite rather pretty even still yet
		already again never always often sometimes usually maybe perhaps probably prob likely
		actually basically literally generally specifically especially exactly almost mostly simply truly
		instead otherwise anyway anyways though although however whether while because cause since unless
		here there where when why how now then today tomorrow yesterday soon later earlier recently currently
		once twice

		one two three four five six seven eight nine ten first second third last next previous former latter

		good great bad better best worse worst nice cool fine big small large little long short high low
		new old right wrong true false real different similar specific certain general whole full entire
		current particular simple easy hard clear sure possible likely able important useful
		based related relevant obvious

		thing things stuff something anything everything nothing way ways kind kinds sort sorts bit part
		parts point points place idea ideas reason reasons question questions answer answers problem
		problems option options approach approaches case cases example examples detail details info
		information impl implementation implementations concept concepts aspect matter fact end

		make makes made making let lets use uses used using add added fix fixed need needs needed want wants
		wanted see saw seen look looks looked looking think thinks thought know knows knew known tell told
		say says said saying ask asks asked mean means meant seem seems seemed feel feels felt guess wonder
		wondering happen happens happened happening go goes went gone going come comes came coming take
		takes took taken give gives gave given keep keeps kept put puts set find finds found help helps
		try tries tried trying start starts started work works worked working change changes changed
		update updates updated check checks checked show shows showed shown run runs ran running
		able going done

		code file here time times day days week weeks today minute minutes hour hours

		lol imo tbh btw idk rn ur u gonna wanna gotta kinda sorta wdym fyi afaik iirc etc eg ie vs

		is it to of in on at do an or if so be we me my no up as by go us re id im ve ll
	`) {
		m[stem(w)] = true
	}
	return m
}()
