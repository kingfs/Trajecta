// Code generated from indicator.rl by ragel. DO NOT EDIT.
//
// Regenerate with:
//
//	task generate:secretmask
//
// Source of truth: pkg/secretmask/indicator.rl
//
// This is a second machine, not part of the credential scanner in rules.rl, and
// the split is deliberate. A Ragel scanner consumes what it matched, so a URL
// rule living in the same machine would swallow every credential inside the URL
// (a presigned AWS URL carries AKIA... in its query string) and the masker would
// never see it. Two machines mean two independent passes over the same bytes:
// one protects credentials, this one reports access indicators.

package secretmask

var indicator_start int = 23
var _ = indicator_start
var indicator_first_final int = 23
var _ = indicator_first_final
var indicator_error int = -1
var _ = indicator_error
var indicator_en_main int = 23
var _ = indicator_en_main

// scanIndicatorDFA runs the report-only access DFA over data, appending every
// candidate to sink. It performs no allocation and no validation beyond the
// rule metadata checks in indicatorCollector.emit; the shape checks that make
// a candidate a URL or a local path live in indicator.go.
func scanIndicatorDFA(data []byte, sink *indicatorCollector) {
	cs, p, pe, eof := 0, 0, len(data), len(data)
	ts, te, act := 0, 0, 0
	_ = act
	_ = te
	// write init is required here: the machine's start state is not 0, and
	// without it `write exec` would enter the state table at state 0 (a middle
	// state) and report spans that neither start nor end where a pattern did.
	{
		cs = int(indicator_start)
		ts = 0
		te = 0
		act = 0

	}

	{
		switch cs {
		case 23:
			goto st_case_23
		case 24:
			goto st_case_24
		case 0:
			goto st_case_0
		case 1:
			goto st_case_1
		case 25:
			goto st_case_25
		case 26:
			goto st_case_26
		case 27:
			goto st_case_27
		case 2:
			goto st_case_2
		case 28:
			goto st_case_28
		case 29:
			goto st_case_29
		case 30:
			goto st_case_30
		case 31:
			goto st_case_31
		case 32:
			goto st_case_32
		case 3:
			goto st_case_3
		case 4:
			goto st_case_4
		case 5:
			goto st_case_5
		case 33:
			goto st_case_33
		case 6:
			goto st_case_6
		case 7:
			goto st_case_7
		case 8:
			goto st_case_8
		case 34:
			goto st_case_34
		case 9:
			goto st_case_9
		case 10:
			goto st_case_10
		case 35:
			goto st_case_35
		case 11:
			goto st_case_11
		case 36:
			goto st_case_36
		case 37:
			goto st_case_37
		case 12:
			goto st_case_12
		case 13:
			goto st_case_13
		case 14:
			goto st_case_14
		case 15:
			goto st_case_15
		case 16:
			goto st_case_16
		case 17:
			goto st_case_17
		case 18:
			goto st_case_18
		case 19:
			goto st_case_19
		case 38:
			goto st_case_38
		case 20:
			goto st_case_20
		case 21:
			goto st_case_21
		case 39:
			goto st_case_39
		case 40:
			goto st_case_40
		case 22:
			goto st_case_22
		case 41:
			goto st_case_41
		case 42:
			goto st_case_42
		case 43:
			goto st_case_43
		case 44:
			goto st_case_44

		}
	_ctr0:
		{
			{
				p = (te) - 1

			}
		}
		goto _st23
	_ctr5:
		{
			{
				switch act {
				case 1:
					p = (te) - 1
					{
						sink.emit(RuleURL, ts, te)
					}

				case 2:
					p = (te) - 1
					{
						sink.emit(RuleLocalPath, ts, te)
					}

				case 3:
					p = (te) - 1
					{
						sink.emit(RuleLocalPath, ts, te)
					}

				case 4:
					p = (te) - 1
					{
						sink.emit(RuleLocalPath, ts, te)
					}

				default:
					p = (te) - 1

				}

			}

		}
		goto _st23
	_ctr28:
		{
			{
				te = p + 1

			}
		}
		goto _st23
	_ctr37:
		{
			{
				te = p
				p = p - 1

			}
		}
		goto _st23
	_ctr38:
		{
			{
				te = p
				p = p - 1
				{
					sink.emit(RuleLocalPath, ts, te)
				}

			}
		}
		goto _st23
	_ctr41:
		{
			{
				te = p
				p = p - 1
				{
					sink.emit(RuleLocalPath, ts, te)
				}

			}
		}
		goto _st23
	_ctr43:
		{
			{
				te = p
				p = p - 1
				{
					sink.emit(RuleLocalPath, ts, te)
				}

			}
		}
		goto _st23
	_ctr47:
		{
			{
				te = p
				p = p - 1
				{
					sink.emit(RuleLocalPath, ts, te)
				}

			}
		}
		goto _st23
	_ctr49:
		{
			{
				te = p
				p = p - 1
				{
					sink.emit(RuleLocalPath, ts, te)
				}

			}
		}
		goto _st23
	_ctr51:
		{
			{
				te = p
				p = p - 1
				{
					sink.emit(RuleURL, ts, te)
				}

			}
		}
		goto _st23
	_ctr54:
		{
			{
				te = p
				p = p - 1
				{
					sink.emit(RuleLocalPath, ts, te)
				}

			}
		}
		goto _st23
	_st23:
		if p == eof {
			goto _out23

		}
		{
			{
				ts = 0

			}
		}
		p += 1
	st_case_23:
		if p == pe && p != eof {
			goto _out23

		}
		{
			{
				ts = p

			}
		}
		if p == eof {
			goto _st23

		} else {
			switch data[p] {
			case 43:
				{
					goto _ctr29

				}
			case 46:
				{
					goto _ctr30

				}
			case 47:
				{
					goto _ctr31

				}
			case 64:
				{
					goto _ctr29

				}
			case 92:
				{
					goto _ctr33

				}
			case 95:
				{
					goto _ctr29

				}
			case 104:
				{
					goto _ctr34

				}
			case 119:
				{
					goto _ctr35

				}
			case 126:
				{
					goto _ctr36

				}

			}
			if (data[p]) < 45 {
				if 36 <= (data[p]) && (data[p]) <= 37 {
					goto _ctr29

				}

			} else if (data[p]) > 57 {
				if (data[p]) > 90 {
					if 97 <= (data[p]) && (data[p]) <= 122 {
						goto _ctr32

					}

				} else if (data[p]) >= 65 {
					goto _ctr32

				}

			} else {
				goto _ctr29

			}
			goto _ctr28

		}
	_ctr29:
		{
			{
				te = p + 1

			}
		}
		goto _st24
	_st24:
		if p == eof {
			goto _out24

		}
		p += 1
	st_case_24:
		if p == pe && p != eof {
			goto _out24

		}
		if p == eof {
			goto _ctr37

		} else {
			switch data[p] {
			case 43:
				{
					goto _st0

				}
			case 47:
				{
					goto _st1

				}
			case 95:
				{
					goto _st0

				}
			case 126:
				{
					goto _st0

				}

			}
			if (data[p]) < 45 {
				if 36 <= (data[p]) && (data[p]) <= 37 {
					goto _st0

				}

			} else if (data[p]) > 57 {
				if (data[p]) > 90 {
					if 97 <= (data[p]) && (data[p]) <= 122 {
						goto _st0

					}

				} else if (data[p]) >= 64 {
					goto _st0

				}

			} else {
				goto _st0

			}
			goto _ctr37

		}
	_st0:
		if p == eof {
			goto _out0

		}
		p += 1
	st_case_0:
		if p == pe && p != eof {
			goto _out0

		}
		if p == eof {
			goto _ctr0

		} else {
			switch data[p] {
			case 43:
				{
					goto _st0

				}
			case 47:
				{
					goto _st1

				}
			case 95:
				{
					goto _st0

				}
			case 126:
				{
					goto _st0

				}

			}
			if (data[p]) < 45 {
				if 36 <= (data[p]) && (data[p]) <= 37 {
					goto _st0

				}

			} else if (data[p]) > 57 {
				if (data[p]) > 90 {
					if 97 <= (data[p]) && (data[p]) <= 122 {
						goto _st0

					}

				} else if (data[p]) >= 64 {
					goto _st0

				}

			} else {
				goto _st0

			}
			goto _ctr0

		}
	_st1:
		if p == eof {
			goto _out1

		}
		p += 1
	st_case_1:
		if p == pe && p != eof {
			goto _out1

		}
		if p == eof {
			goto _ctr0

		} else {
			switch data[p] {
			case 43:
				{
					goto _st25

				}
			case 95:
				{
					goto _st25

				}
			case 126:
				{
					goto _st25

				}

			}
			if (data[p]) < 48 {
				if (data[p]) > 37 {
					if 45 <= (data[p]) && (data[p]) <= 46 {
						goto _st25

					}

				} else if (data[p]) >= 36 {
					goto _st25

				}

			} else if (data[p]) > 57 {
				if (data[p]) > 90 {
					if 97 <= (data[p]) && (data[p]) <= 122 {
						goto _st25

					}

				} else if (data[p]) >= 64 {
					goto _st25

				}

			} else {
				goto _st25

			}
			goto _ctr0

		}
	_st25:
		if p == eof {
			goto _out25

		}
		p += 1
	st_case_25:
		if p == pe && p != eof {
			goto _out25

		}
		if p == eof {
			goto _ctr38

		} else {
			switch data[p] {
			case 43:
				{
					goto _st25

				}
			case 47:
				{
					goto _st26

				}
			case 95:
				{
					goto _st25

				}
			case 126:
				{
					goto _st25

				}

			}
			if (data[p]) < 45 {
				if 36 <= (data[p]) && (data[p]) <= 37 {
					goto _st25

				}

			} else if (data[p]) > 57 {
				if (data[p]) > 90 {
					if 97 <= (data[p]) && (data[p]) <= 122 {
						goto _st25

					}

				} else if (data[p]) >= 64 {
					goto _st25

				}

			} else {
				goto _st25

			}
			goto _ctr38

		}
	_st26:
		if p == eof {
			goto _out26

		}
		p += 1
	st_case_26:
		if p == pe && p != eof {
			goto _out26

		}
		if p == eof {
			goto _ctr38

		} else {
			switch data[p] {
			case 43:
				{
					goto _st25

				}
			case 95:
				{
					goto _st25

				}
			case 126:
				{
					goto _st25

				}

			}
			if (data[p]) < 48 {
				if (data[p]) > 37 {
					if 45 <= (data[p]) && (data[p]) <= 46 {
						goto _st25

					}

				} else if (data[p]) >= 36 {
					goto _st25

				}

			} else if (data[p]) > 57 {
				if (data[p]) > 90 {
					if 97 <= (data[p]) && (data[p]) <= 122 {
						goto _st25

					}

				} else if (data[p]) >= 64 {
					goto _st25

				}

			} else {
				goto _st25

			}
			goto _ctr38

		}
	_ctr30:
		{
			{
				te = p + 1

			}
		}
		goto _st27
	_st27:
		if p == eof {
			goto _out27

		}
		p += 1
	st_case_27:
		if p == pe && p != eof {
			goto _out27

		}
		if p == eof {
			goto _ctr37

		} else {
			switch data[p] {
			case 43:
				{
					goto _st0

				}
			case 46:
				{
					goto _st2

				}
			case 47:
				{
					goto _st28

				}
			case 95:
				{
					goto _st0

				}
			case 126:
				{
					goto _st0

				}

			}
			if (data[p]) < 45 {
				if 36 <= (data[p]) && (data[p]) <= 37 {
					goto _st0

				}

			} else if (data[p]) > 57 {
				if (data[p]) > 90 {
					if 97 <= (data[p]) && (data[p]) <= 122 {
						goto _st0

					}

				} else if (data[p]) >= 64 {
					goto _st0

				}

			} else {
				goto _st0

			}
			goto _ctr37

		}
	_st2:
		if p == eof {
			goto _out2

		}
		p += 1
	st_case_2:
		if p == pe && p != eof {
			goto _out2

		}
		if p == eof {
			goto _ctr0

		} else {
			switch data[p] {
			case 43:
				{
					goto _st0

				}
			case 47:
				{
					goto _st28

				}
			case 95:
				{
					goto _st0

				}
			case 126:
				{
					goto _st0

				}

			}
			if (data[p]) < 45 {
				if 36 <= (data[p]) && (data[p]) <= 37 {
					goto _st0

				}

			} else if (data[p]) > 57 {
				if (data[p]) > 90 {
					if 97 <= (data[p]) && (data[p]) <= 122 {
						goto _st0

					}

				} else if (data[p]) >= 64 {
					goto _st0

				}

			} else {
				goto _st0

			}
			goto _ctr0

		}
	_st28:
		if p == eof {
			goto _out28

		}
		p += 1
	st_case_28:
		if p == pe && p != eof {
			goto _out28

		}
		if p == eof {
			goto _ctr41

		} else {
			switch data[p] {
			case 43:
				{
					goto _st29

				}
			case 95:
				{
					goto _st29

				}
			case 126:
				{
					goto _st29

				}

			}
			if (data[p]) < 48 {
				if (data[p]) > 37 {
					if 45 <= (data[p]) && (data[p]) <= 46 {
						goto _st29

					}

				} else if (data[p]) >= 36 {
					goto _st29

				}

			} else if (data[p]) > 57 {
				if (data[p]) > 90 {
					if 97 <= (data[p]) && (data[p]) <= 122 {
						goto _st29

					}

				} else if (data[p]) >= 64 {
					goto _st29

				}

			} else {
				goto _st29

			}
			goto _ctr41

		}
	_st29:
		if p == eof {
			goto _out29

		}
		p += 1
	st_case_29:
		if p == pe && p != eof {
			goto _out29

		}
		if p == eof {
			goto _ctr41

		} else {
			switch data[p] {
			case 43:
				{
					goto _st29

				}
			case 47:
				{
					goto _st28

				}
			case 95:
				{
					goto _st29

				}
			case 126:
				{
					goto _st29

				}

			}
			if (data[p]) < 45 {
				if 36 <= (data[p]) && (data[p]) <= 37 {
					goto _st29

				}

			} else if (data[p]) > 57 {
				if (data[p]) > 90 {
					if 97 <= (data[p]) && (data[p]) <= 122 {
						goto _st29

					}

				} else if (data[p]) >= 64 {
					goto _st29

				}

			} else {
				goto _st29

			}
			goto _ctr41

		}
	_ctr31:
		{
			{
				te = p + 1

			}
		}
		{
			{
				act = 4

			}
		}
		goto _st30
	_st30:
		if p == eof {
			goto _out30

		}
		p += 1
	st_case_30:
		if p == pe && p != eof {
			goto _out30

		}
		if p == eof {
			goto _ctr43

		} else {
			switch data[p] {
			case 43:
				{
					goto _st31

				}
			case 47:
				{
					goto _st3

				}
			case 92:
				{
					goto _st3

				}
			case 95:
				{
					goto _st31

				}
			case 126:
				{
					goto _st31

				}

			}
			if (data[p]) < 45 {
				if 36 <= (data[p]) && (data[p]) <= 37 {
					goto _st31

				}

			} else if (data[p]) > 57 {
				if (data[p]) > 90 {
					if 97 <= (data[p]) && (data[p]) <= 122 {
						goto _st31

					}

				} else if (data[p]) >= 64 {
					goto _st31

				}

			} else {
				goto _st31

			}
			goto _ctr43

		}
	_st31:
		if p == eof {
			goto _out31

		}
		p += 1
	st_case_31:
		if p == pe && p != eof {
			goto _out31

		}
		if p == eof {
			goto _ctr43

		} else {
			switch data[p] {
			case 43:
				{
					goto _st31

				}
			case 47:
				{
					goto _st32

				}
			case 95:
				{
					goto _st31

				}
			case 126:
				{
					goto _st31

				}

			}
			if (data[p]) < 45 {
				if 36 <= (data[p]) && (data[p]) <= 37 {
					goto _st31

				}

			} else if (data[p]) > 57 {
				if (data[p]) > 90 {
					if 97 <= (data[p]) && (data[p]) <= 122 {
						goto _st31

					}

				} else if (data[p]) >= 64 {
					goto _st31

				}

			} else {
				goto _st31

			}
			goto _ctr43

		}
	_st32:
		if p == eof {
			goto _out32

		}
		p += 1
	st_case_32:
		if p == pe && p != eof {
			goto _out32

		}
		if p == eof {
			goto _ctr43

		} else {
			switch data[p] {
			case 43:
				{
					goto _st31

				}
			case 95:
				{
					goto _st31

				}
			case 126:
				{
					goto _st31

				}

			}
			if (data[p]) < 48 {
				if (data[p]) > 37 {
					if 45 <= (data[p]) && (data[p]) <= 46 {
						goto _st31

					}

				} else if (data[p]) >= 36 {
					goto _st31

				}

			} else if (data[p]) > 57 {
				if (data[p]) > 90 {
					if 97 <= (data[p]) && (data[p]) <= 122 {
						goto _st31

					}

				} else if (data[p]) >= 64 {
					goto _st31

				}

			} else {
				goto _st31

			}
			goto _ctr43

		}
	_st3:
		if p == eof {
			goto _out3

		}
		p += 1
	st_case_3:
		if p == pe && p != eof {
			goto _out3

		}
		if p == eof {
			goto _ctr5

		} else {
			switch data[p] {
			case 43:
				{
					goto _st4

				}
			case 47:
				{
					goto _st7

				}
			case 92:
				{
					goto _st7

				}
			case 95:
				{
					goto _st4

				}
			case 126:
				{
					goto _st4

				}

			}
			if (data[p]) < 45 {
				if 36 <= (data[p]) && (data[p]) <= 37 {
					goto _st4

				}

			} else if (data[p]) > 57 {
				if (data[p]) > 90 {
					if 97 <= (data[p]) && (data[p]) <= 122 {
						goto _st4

					}

				} else if (data[p]) >= 64 {
					goto _st4

				}

			} else {
				goto _st4

			}
			goto _ctr5

		}
	_st4:
		if p == eof {
			goto _out4

		}
		p += 1
	st_case_4:
		if p == pe && p != eof {
			goto _out4

		}
		if p == eof {
			goto _ctr5

		} else {
			switch data[p] {
			case 43:
				{
					goto _st4

				}
			case 47:
				{
					goto _st5

				}
			case 92:
				{
					goto _st5

				}
			case 95:
				{
					goto _st4

				}
			case 126:
				{
					goto _st4

				}

			}
			if (data[p]) < 45 {
				if 36 <= (data[p]) && (data[p]) <= 37 {
					goto _st4

				}

			} else if (data[p]) > 57 {
				if (data[p]) > 90 {
					if 97 <= (data[p]) && (data[p]) <= 122 {
						goto _st4

					}

				} else if (data[p]) >= 64 {
					goto _st4

				}

			} else {
				goto _st4

			}
			goto _ctr5

		}
	_st5:
		if p == eof {
			goto _out5

		}
		p += 1
	st_case_5:
		if p == pe && p != eof {
			goto _out5

		}
		if p == eof {
			goto _ctr5

		} else {
			switch data[p] {
			case 43:
				{
					goto _ctr9

				}
			case 47:
				{
					goto _st6

				}
			case 92:
				{
					goto _st6

				}
			case 95:
				{
					goto _ctr9

				}
			case 126:
				{
					goto _ctr9

				}

			}
			if (data[p]) < 45 {
				if 36 <= (data[p]) && (data[p]) <= 37 {
					goto _ctr9

				}

			} else if (data[p]) > 57 {
				if (data[p]) > 90 {
					if 97 <= (data[p]) && (data[p]) <= 122 {
						goto _ctr9

					}

				} else if (data[p]) >= 64 {
					goto _ctr9

				}

			} else {
				goto _ctr9

			}
			goto _ctr5

		}
	_ctr9:
		{
			{
				te = p + 1

			}
		}
		{
			{
				act = 3

			}
		}
		goto _st33
	_st33:
		if p == eof {
			goto _out33

		}
		p += 1
	st_case_33:
		if p == pe && p != eof {
			goto _out33

		}
		if p == eof {
			goto _ctr47

		} else {
			switch data[p] {
			case 43:
				{
					goto _ctr9

				}
			case 47:
				{
					goto _st5

				}
			case 92:
				{
					goto _st5

				}
			case 95:
				{
					goto _ctr9

				}
			case 126:
				{
					goto _ctr9

				}

			}
			if (data[p]) < 45 {
				if 36 <= (data[p]) && (data[p]) <= 37 {
					goto _ctr9

				}

			} else if (data[p]) > 57 {
				if (data[p]) > 90 {
					if 97 <= (data[p]) && (data[p]) <= 122 {
						goto _ctr9

					}

				} else if (data[p]) >= 64 {
					goto _ctr9

				}

			} else {
				goto _ctr9

			}
			goto _ctr47

		}
	_st6:
		if p == eof {
			goto _out6

		}
		p += 1
	st_case_6:
		if p == pe && p != eof {
			goto _out6

		}
		if p == eof {
			goto _ctr5

		} else {
			switch data[p] {
			case 43:
				{
					goto _ctr9

				}
			case 95:
				{
					goto _ctr9

				}
			case 126:
				{
					goto _ctr9

				}

			}
			if (data[p]) < 48 {
				if (data[p]) > 37 {
					if 45 <= (data[p]) && (data[p]) <= 46 {
						goto _ctr9

					}

				} else if (data[p]) >= 36 {
					goto _ctr9

				}

			} else if (data[p]) > 57 {
				if (data[p]) > 90 {
					if 97 <= (data[p]) && (data[p]) <= 122 {
						goto _ctr9

					}

				} else if (data[p]) >= 64 {
					goto _ctr9

				}

			} else {
				goto _ctr9

			}
			goto _ctr5

		}
	_st7:
		if p == eof {
			goto _out7

		}
		p += 1
	st_case_7:
		if p == pe && p != eof {
			goto _out7

		}
		if p == eof {
			goto _ctr5

		} else {
			switch data[p] {
			case 47:
				{
					goto _st8

				}
			case 92:
				{
					goto _st8

				}

			}
			goto _ctr5

		}
	_st8:
		if p == eof {
			goto _out8

		}
		p += 1
	st_case_8:
		if p == pe && p != eof {
			goto _out8

		}
		if p == eof {
			goto _ctr5

		} else {
			switch data[p] {
			case 43:
				{
					goto _st4

				}
			case 95:
				{
					goto _st4

				}
			case 126:
				{
					goto _st4

				}

			}
			if (data[p]) < 48 {
				if (data[p]) > 37 {
					if 45 <= (data[p]) && (data[p]) <= 46 {
						goto _st4

					}

				} else if (data[p]) >= 36 {
					goto _st4

				}

			} else if (data[p]) > 57 {
				if (data[p]) > 90 {
					if 97 <= (data[p]) && (data[p]) <= 122 {
						goto _st4

					}

				} else if (data[p]) >= 64 {
					goto _st4

				}

			} else {
				goto _st4

			}
			goto _ctr5

		}
	_ctr32:
		{
			{
				te = p + 1

			}
		}
		{
			{
				act = 8

			}
		}
		goto _st34
	_st34:
		if p == eof {
			goto _out34

		}
		p += 1
	st_case_34:
		if p == pe && p != eof {
			goto _out34

		}
		if p == eof {
			goto _ctr37

		} else {
			switch data[p] {
			case 43:
				{
					goto _st0

				}
			case 47:
				{
					goto _st1

				}
			case 58:
				{
					goto _st9

				}
			case 95:
				{
					goto _st0

				}
			case 126:
				{
					goto _st0

				}

			}
			if (data[p]) < 45 {
				if 36 <= (data[p]) && (data[p]) <= 37 {
					goto _st0

				}

			} else if (data[p]) > 57 {
				if (data[p]) > 90 {
					if 97 <= (data[p]) && (data[p]) <= 122 {
						goto _st0

					}

				} else if (data[p]) >= 64 {
					goto _st0

				}

			} else {
				goto _st0

			}
			goto _ctr37

		}
	_st9:
		if p == eof {
			goto _out9

		}
		p += 1
	st_case_9:
		if p == pe && p != eof {
			goto _out9

		}
		if p == eof {
			goto _ctr0

		} else {
			switch data[p] {
			case 47:
				{
					goto _st10

				}
			case 92:
				{
					goto _st10

				}

			}
			goto _ctr0

		}
	_st10:
		if p == eof {
			goto _out10

		}
		p += 1
	st_case_10:
		if p == pe && p != eof {
			goto _out10

		}
		if p == eof {
			goto _ctr5

		} else {
			switch data[p] {
			case 43:
				{
					goto _ctr13

				}
			case 47:
				{
					goto _st11

				}
			case 92:
				{
					goto _st11

				}
			case 95:
				{
					goto _ctr13

				}
			case 126:
				{
					goto _ctr13

				}

			}
			if (data[p]) < 45 {
				if 36 <= (data[p]) && (data[p]) <= 37 {
					goto _ctr13

				}

			} else if (data[p]) > 57 {
				if (data[p]) > 90 {
					if 97 <= (data[p]) && (data[p]) <= 122 {
						goto _ctr13

					}

				} else if (data[p]) >= 64 {
					goto _ctr13

				}

			} else {
				goto _ctr13

			}
			goto _ctr5

		}
	_ctr13:
		{
			{
				te = p + 1

			}
		}
		{
			{
				act = 2

			}
		}
		goto _st35
	_st35:
		if p == eof {
			goto _out35

		}
		p += 1
	st_case_35:
		if p == pe && p != eof {
			goto _out35

		}
		if p == eof {
			goto _ctr49

		} else {
			switch data[p] {
			case 43:
				{
					goto _ctr13

				}
			case 47:
				{
					goto _st10

				}
			case 92:
				{
					goto _st10

				}
			case 95:
				{
					goto _ctr13

				}
			case 126:
				{
					goto _ctr13

				}

			}
			if (data[p]) < 45 {
				if 36 <= (data[p]) && (data[p]) <= 37 {
					goto _ctr13

				}

			} else if (data[p]) > 57 {
				if (data[p]) > 90 {
					if 97 <= (data[p]) && (data[p]) <= 122 {
						goto _ctr13

					}

				} else if (data[p]) >= 64 {
					goto _ctr13

				}

			} else {
				goto _ctr13

			}
			goto _ctr49

		}
	_st11:
		if p == eof {
			goto _out11

		}
		p += 1
	st_case_11:
		if p == pe && p != eof {
			goto _out11

		}
		if p == eof {
			goto _ctr5

		} else {
			switch data[p] {
			case 43:
				{
					goto _ctr13

				}
			case 95:
				{
					goto _ctr13

				}
			case 126:
				{
					goto _ctr13

				}

			}
			if (data[p]) < 48 {
				if (data[p]) > 37 {
					if 45 <= (data[p]) && (data[p]) <= 46 {
						goto _ctr13

					}

				} else if (data[p]) >= 36 {
					goto _ctr13

				}

			} else if (data[p]) > 57 {
				if (data[p]) > 90 {
					if 97 <= (data[p]) && (data[p]) <= 122 {
						goto _ctr13

					}

				} else if (data[p]) >= 64 {
					goto _ctr13

				}

			} else {
				goto _ctr13

			}
			goto _ctr5

		}
	_ctr33:
		{
			{
				te = p + 1

			}
		}
		{
			{
				act = 8

			}
		}
		goto _st36
	_st36:
		if p == eof {
			goto _out36

		}
		p += 1
	st_case_36:
		if p == pe && p != eof {
			goto _out36

		}
		if p == eof {
			goto _ctr37

		} else {
			switch data[p] {
			case 47:
				{
					goto _st3

				}
			case 92:
				{
					goto _st3

				}

			}
			goto _ctr37

		}
	_ctr34:
		{
			{
				te = p + 1

			}
		}
		{
			{
				act = 8

			}
		}
		goto _st37
	_st37:
		if p == eof {
			goto _out37

		}
		p += 1
	st_case_37:
		if p == pe && p != eof {
			goto _out37

		}
		if p == eof {
			goto _ctr37

		} else {
			switch data[p] {
			case 43:
				{
					goto _st0

				}
			case 47:
				{
					goto _st1

				}
			case 58:
				{
					goto _st9

				}
			case 95:
				{
					goto _st0

				}
			case 116:
				{
					goto _st12

				}
			case 126:
				{
					goto _st0

				}

			}
			if (data[p]) < 45 {
				if 36 <= (data[p]) && (data[p]) <= 37 {
					goto _st0

				}

			} else if (data[p]) > 57 {
				if (data[p]) > 90 {
					if 97 <= (data[p]) && (data[p]) <= 122 {
						goto _st0

					}

				} else if (data[p]) >= 64 {
					goto _st0

				}

			} else {
				goto _st0

			}
			goto _ctr37

		}
	_st12:
		if p == eof {
			goto _out12

		}
		p += 1
	st_case_12:
		if p == pe && p != eof {
			goto _out12

		}
		if p == eof {
			goto _ctr0

		} else {
			switch data[p] {
			case 43:
				{
					goto _st0

				}
			case 47:
				{
					goto _st1

				}
			case 95:
				{
					goto _st0

				}
			case 116:
				{
					goto _st13

				}
			case 126:
				{
					goto _st0

				}

			}
			if (data[p]) < 45 {
				if 36 <= (data[p]) && (data[p]) <= 37 {
					goto _st0

				}

			} else if (data[p]) > 57 {
				if (data[p]) > 90 {
					if 97 <= (data[p]) && (data[p]) <= 122 {
						goto _st0

					}

				} else if (data[p]) >= 64 {
					goto _st0

				}

			} else {
				goto _st0

			}
			goto _ctr0

		}
	_st13:
		if p == eof {
			goto _out13

		}
		p += 1
	st_case_13:
		if p == pe && p != eof {
			goto _out13

		}
		if p == eof {
			goto _ctr0

		} else {
			switch data[p] {
			case 43:
				{
					goto _st0

				}
			case 47:
				{
					goto _st1

				}
			case 95:
				{
					goto _st0

				}
			case 112:
				{
					goto _st14

				}
			case 126:
				{
					goto _st0

				}

			}
			if (data[p]) < 45 {
				if 36 <= (data[p]) && (data[p]) <= 37 {
					goto _st0

				}

			} else if (data[p]) > 57 {
				if (data[p]) > 90 {
					if 97 <= (data[p]) && (data[p]) <= 122 {
						goto _st0

					}

				} else if (data[p]) >= 64 {
					goto _st0

				}

			} else {
				goto _st0

			}
			goto _ctr0

		}
	_st14:
		if p == eof {
			goto _out14

		}
		p += 1
	st_case_14:
		if p == pe && p != eof {
			goto _out14

		}
		if p == eof {
			goto _ctr0

		} else {
			switch data[p] {
			case 43:
				{
					goto _st0

				}
			case 47:
				{
					goto _st1

				}
			case 58:
				{
					goto _st15

				}
			case 95:
				{
					goto _st0

				}
			case 115:
				{
					goto _st22

				}
			case 126:
				{
					goto _st0

				}

			}
			if (data[p]) < 45 {
				if 36 <= (data[p]) && (data[p]) <= 37 {
					goto _st0

				}

			} else if (data[p]) > 57 {
				if (data[p]) > 90 {
					if 97 <= (data[p]) && (data[p]) <= 122 {
						goto _st0

					}

				} else if (data[p]) >= 64 {
					goto _st0

				}

			} else {
				goto _st0

			}
			goto _ctr0

		}
	_st15:
		if p == eof {
			goto _out15

		}
		p += 1
	st_case_15:
		if p == pe && p != eof {
			goto _out15

		}
		if p == eof {
			goto _ctr0

		} else {
			if (data[p]) == 47 {
				goto _st16

			}
			goto _ctr0

		}
	_st16:
		if p == eof {
			goto _out16

		}
		p += 1
	st_case_16:
		if p == pe && p != eof {
			goto _out16

		}
		if p == eof {
			goto _ctr0

		} else {
			if (data[p]) == 47 {
				goto _st17

			}
			goto _ctr0

		}
	_st17:
		if p == eof {
			goto _out17

		}
		p += 1
	st_case_17:
		if p == pe && p != eof {
			goto _out17

		}
		if p == eof {
			goto _ctr0

		} else {
			switch data[p] {
			case 33:
				{
					goto _st18

				}
			case 61:
				{
					goto _st18

				}
			case 91:
				{
					goto _st20

				}
			case 95:
				{
					goto _st39

				}
			case 126:
				{
					goto _st39

				}

			}
			if (data[p]) < 48 {
				if (data[p]) < 42 {
					if 36 <= (data[p]) && (data[p]) <= 39 {
						goto _st18

					}

				} else if (data[p]) > 44 {
					if (data[p]) <= 46 {
						goto _st39

					}

				} else {
					goto _st18

				}

			} else if (data[p]) > 57 {
				if (data[p]) < 65 {
					if (data[p]) <= 59 {
						goto _st18

					}

				} else if (data[p]) > 90 {
					if 97 <= (data[p]) && (data[p]) <= 122 {
						goto _st39

					}

				} else {
					goto _st39

				}

			} else {
				goto _st39

			}
			goto _ctr0

		}
	_st18:
		if p == eof {
			goto _out18

		}
		p += 1
	st_case_18:
		if p == pe && p != eof {
			goto _out18

		}
		if p == eof {
			goto _ctr0

		} else {
			switch data[p] {
			case 33:
				{
					goto _st18

				}
			case 61:
				{
					goto _st18

				}
			case 64:
				{
					goto _st19

				}
			case 95:
				{
					goto _st18

				}
			case 126:
				{
					goto _st18

				}

			}
			if (data[p]) < 48 {
				if (data[p]) > 39 {
					if 42 <= (data[p]) && (data[p]) <= 46 {
						goto _st18

					}

				} else if (data[p]) >= 36 {
					goto _st18

				}

			} else if (data[p]) > 59 {
				if (data[p]) > 90 {
					if 97 <= (data[p]) && (data[p]) <= 122 {
						goto _st18

					}

				} else if (data[p]) >= 65 {
					goto _st18

				}

			} else {
				goto _st18

			}
			goto _ctr0

		}
	_st19:
		if p == eof {
			goto _out19

		}
		p += 1
	st_case_19:
		if p == pe && p != eof {
			goto _out19

		}
		if p == eof {
			goto _ctr0

		} else {
			switch data[p] {
			case 91:
				{
					goto _st20

				}
			case 95:
				{
					goto _st38

				}
			case 126:
				{
					goto _st38

				}

			}
			if (data[p]) < 48 {
				if 45 <= (data[p]) && (data[p]) <= 46 {
					goto _st38

				}

			} else if (data[p]) > 57 {
				if (data[p]) > 90 {
					if 97 <= (data[p]) && (data[p]) <= 122 {
						goto _st38

					}

				} else if (data[p]) >= 65 {
					goto _st38

				}

			} else {
				goto _st38

			}
			goto _ctr0

		}
	_st38:
		if p == eof {
			goto _out38

		}
		p += 1
	st_case_38:
		if p == pe && p != eof {
			goto _out38

		}
		if p == eof {
			goto _ctr51

		} else {
			switch data[p] {
			case 33:
				{
					goto _st38

				}
			case 61:
				{
					goto _st38

				}
			case 95:
				{
					goto _st38

				}
			case 126:
				{
					goto _st38

				}

			}
			if (data[p]) < 63 {
				if 35 <= (data[p]) && (data[p]) <= 59 {
					goto _st38

				}

			} else if (data[p]) > 90 {
				if 97 <= (data[p]) && (data[p]) <= 122 {
					goto _st38

				}

			} else {
				goto _st38

			}
			goto _ctr51

		}
	_st20:
		if p == eof {
			goto _out20

		}
		p += 1
	st_case_20:
		if p == pe && p != eof {
			goto _out20

		}
		if p == eof {
			goto _ctr5

		} else {
			if (data[p]) == 46 {
				goto _st21

			}
			if (data[p]) < 65 {
				if 48 <= (data[p]) && (data[p]) <= 58 {
					goto _st21

				}

			} else if (data[p]) > 70 {
				if 97 <= (data[p]) && (data[p]) <= 102 {
					goto _st21

				}

			} else {
				goto _st21

			}
			goto _ctr5

		}
	_st21:
		if p == eof {
			goto _out21

		}
		p += 1
	st_case_21:
		if p == pe && p != eof {
			goto _out21

		}
		if p == eof {
			goto _ctr5

		} else {
			switch data[p] {
			case 46:
				{
					goto _st21

				}
			case 93:
				{
					goto _st38

				}

			}
			if (data[p]) < 65 {
				if 48 <= (data[p]) && (data[p]) <= 58 {
					goto _st21

				}

			} else if (data[p]) > 70 {
				if 97 <= (data[p]) && (data[p]) <= 102 {
					goto _st21

				}

			} else {
				goto _st21

			}
			goto _ctr5

		}
	_st39:
		if p == eof {
			goto _out39

		}
		p += 1
	st_case_39:
		if p == pe && p != eof {
			goto _out39

		}
		if p == eof {
			goto _ctr51

		} else {
			switch data[p] {
			case 33:
				{
					goto _st39

				}
			case 35:
				{
					goto _st38

				}
			case 47:
				{
					goto _st38

				}
			case 61:
				{
					goto _st39

				}
			case 63:
				{
					goto _st38

				}
			case 64:
				{
					goto _ctr52

				}
			case 95:
				{
					goto _st39

				}
			case 126:
				{
					goto _st39

				}

			}
			if (data[p]) < 42 {
				if (data[p]) > 39 {
					{
						goto _st38

					}

				} else if (data[p]) >= 36 {
					goto _st39

				}

			} else if (data[p]) > 59 {
				if (data[p]) > 90 {
					if 97 <= (data[p]) && (data[p]) <= 122 {
						goto _st39

					}

				} else if (data[p]) >= 65 {
					goto _st39

				}

			} else {
				goto _st39

			}
			goto _ctr51

		}
	_ctr52:
		{
			{
				te = p + 1

			}
		}
		{
			{
				act = 1

			}
		}
		goto _st40
	_st40:
		if p == eof {
			goto _out40

		}
		p += 1
	st_case_40:
		if p == pe && p != eof {
			goto _out40

		}
		if p == eof {
			goto _ctr51

		} else {
			switch data[p] {
			case 33:
				{
					goto _st38

				}
			case 61:
				{
					goto _st38

				}
			case 91:
				{
					goto _st20

				}
			case 95:
				{
					goto _st38

				}
			case 126:
				{
					goto _st38

				}

			}
			if (data[p]) < 63 {
				if 35 <= (data[p]) && (data[p]) <= 59 {
					goto _st38

				}

			} else if (data[p]) > 90 {
				if 97 <= (data[p]) && (data[p]) <= 122 {
					goto _st38

				}

			} else {
				goto _st38

			}
			goto _ctr51

		}
	_st22:
		if p == eof {
			goto _out22

		}
		p += 1
	st_case_22:
		if p == pe && p != eof {
			goto _out22

		}
		if p == eof {
			goto _ctr0

		} else {
			switch data[p] {
			case 43:
				{
					goto _st0

				}
			case 47:
				{
					goto _st1

				}
			case 58:
				{
					goto _st15

				}
			case 95:
				{
					goto _st0

				}
			case 126:
				{
					goto _st0

				}

			}
			if (data[p]) < 45 {
				if 36 <= (data[p]) && (data[p]) <= 37 {
					goto _st0

				}

			} else if (data[p]) > 57 {
				if (data[p]) > 90 {
					if 97 <= (data[p]) && (data[p]) <= 122 {
						goto _st0

					}

				} else if (data[p]) >= 64 {
					goto _st0

				}

			} else {
				goto _st0

			}
			goto _ctr0

		}
	_ctr35:
		{
			{
				te = p + 1

			}
		}
		{
			{
				act = 8

			}
		}
		goto _st41
	_st41:
		if p == eof {
			goto _out41

		}
		p += 1
	st_case_41:
		if p == pe && p != eof {
			goto _out41

		}
		if p == eof {
			goto _ctr37

		} else {
			switch data[p] {
			case 43:
				{
					goto _st0

				}
			case 47:
				{
					goto _st1

				}
			case 58:
				{
					goto _st9

				}
			case 95:
				{
					goto _st0

				}
			case 115:
				{
					goto _st14

				}
			case 126:
				{
					goto _st0

				}

			}
			if (data[p]) < 45 {
				if 36 <= (data[p]) && (data[p]) <= 37 {
					goto _st0

				}

			} else if (data[p]) > 57 {
				if (data[p]) > 90 {
					if 97 <= (data[p]) && (data[p]) <= 122 {
						goto _st0

					}

				} else if (data[p]) >= 64 {
					goto _st0

				}

			} else {
				goto _st0

			}
			goto _ctr37

		}
	_ctr36:
		{
			{
				te = p + 1

			}
		}
		goto _st42
	_st42:
		if p == eof {
			goto _out42

		}
		p += 1
	st_case_42:
		if p == pe && p != eof {
			goto _out42

		}
		if p == eof {
			goto _ctr37

		} else {
			switch data[p] {
			case 43:
				{
					goto _st0

				}
			case 47:
				{
					goto _st43

				}
			case 95:
				{
					goto _st0

				}
			case 126:
				{
					goto _st0

				}

			}
			if (data[p]) < 45 {
				if 36 <= (data[p]) && (data[p]) <= 37 {
					goto _st0

				}

			} else if (data[p]) > 57 {
				if (data[p]) > 90 {
					if 97 <= (data[p]) && (data[p]) <= 122 {
						goto _st0

					}

				} else if (data[p]) >= 64 {
					goto _st0

				}

			} else {
				goto _st0

			}
			goto _ctr37

		}
	_st43:
		if p == eof {
			goto _out43

		}
		p += 1
	st_case_43:
		if p == pe && p != eof {
			goto _out43

		}
		if p == eof {
			goto _ctr54

		} else {
			switch data[p] {
			case 43:
				{
					goto _st44

				}
			case 95:
				{
					goto _st44

				}
			case 126:
				{
					goto _st44

				}

			}
			if (data[p]) < 48 {
				if (data[p]) > 37 {
					if 45 <= (data[p]) && (data[p]) <= 46 {
						goto _st44

					}

				} else if (data[p]) >= 36 {
					goto _st44

				}

			} else if (data[p]) > 57 {
				if (data[p]) > 90 {
					if 97 <= (data[p]) && (data[p]) <= 122 {
						goto _st44

					}

				} else if (data[p]) >= 64 {
					goto _st44

				}

			} else {
				goto _st44

			}
			goto _ctr54

		}
	_st44:
		if p == eof {
			goto _out44

		}
		p += 1
	st_case_44:
		if p == pe && p != eof {
			goto _out44

		}
		if p == eof {
			goto _ctr54

		} else {
			switch data[p] {
			case 43:
				{
					goto _st44

				}
			case 47:
				{
					goto _st43

				}
			case 95:
				{
					goto _st44

				}
			case 126:
				{
					goto _st44

				}

			}
			if (data[p]) < 45 {
				if 36 <= (data[p]) && (data[p]) <= 37 {
					goto _st44

				}

			} else if (data[p]) > 57 {
				if (data[p]) > 90 {
					if 97 <= (data[p]) && (data[p]) <= 122 {
						goto _st44

					}

				} else if (data[p]) >= 64 {
					goto _st44

				}

			} else {
				goto _st44

			}
			goto _ctr54

		}
	_out23:
		cs = 23
		goto _out
	_out24:
		cs = 24
		goto _out
	_out0:
		cs = 0
		goto _out
	_out1:
		cs = 1
		goto _out
	_out25:
		cs = 25
		goto _out
	_out26:
		cs = 26
		goto _out
	_out27:
		cs = 27
		goto _out
	_out2:
		cs = 2
		goto _out
	_out28:
		cs = 28
		goto _out
	_out29:
		cs = 29
		goto _out
	_out30:
		cs = 30
		goto _out
	_out31:
		cs = 31
		goto _out
	_out32:
		cs = 32
		goto _out
	_out3:
		cs = 3
		goto _out
	_out4:
		cs = 4
		goto _out
	_out5:
		cs = 5
		goto _out
	_out33:
		cs = 33
		goto _out
	_out6:
		cs = 6
		goto _out
	_out7:
		cs = 7
		goto _out
	_out8:
		cs = 8
		goto _out
	_out34:
		cs = 34
		goto _out
	_out9:
		cs = 9
		goto _out
	_out10:
		cs = 10
		goto _out
	_out35:
		cs = 35
		goto _out
	_out11:
		cs = 11
		goto _out
	_out36:
		cs = 36
		goto _out
	_out37:
		cs = 37
		goto _out
	_out12:
		cs = 12
		goto _out
	_out13:
		cs = 13
		goto _out
	_out14:
		cs = 14
		goto _out
	_out15:
		cs = 15
		goto _out
	_out16:
		cs = 16
		goto _out
	_out17:
		cs = 17
		goto _out
	_out18:
		cs = 18
		goto _out
	_out19:
		cs = 19
		goto _out
	_out38:
		cs = 38
		goto _out
	_out20:
		cs = 20
		goto _out
	_out21:
		cs = 21
		goto _out
	_out39:
		cs = 39
		goto _out
	_out40:
		cs = 40
		goto _out
	_out22:
		cs = 22
		goto _out
	_out41:
		cs = 41
		goto _out
	_out42:
		cs = 42
		goto _out
	_out43:
		cs = 43
		goto _out
	_out44:
		cs = 44
		goto _out
	_out:
		{

		}

	}

	_ = cs
	_ = p
	_ = pe
	_ = eof
	_ = ts
}
