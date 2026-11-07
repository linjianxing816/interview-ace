package parse

import "strings"

// 校验与清洗的阈值。这些数字不是随便定的：
//   - 一份简历里单个字段超过 300 字，几乎一定是模型把整段话塞进来了
//   - 一个项目超过 10 条要点，正常简历不会这样写
//   - 技能超过 50 条，多半是把句子拆成了词
//
// 超了就截断而不是报错——简历还是能用的，只是要防住模型跑偏。
const (
	maxFieldRunes = 300
	maxDetails    = 10
	maxSkills     = 50
)

// normalize 清洗模型返回的结构化结果。
//
// 模型会犯三类毛病，这里逐条处理：
//  1. 字段首尾带空格或换行
//  2. 返回一堆空对象（{"school":"","major":""}），看着有内容其实没用
//  3. 单个字段异常长，把整段原文塞进来
//
// 清洗后可能变成空结果，调用方用 IsEmpty 判断。
func normalize(p Parsed) Parsed {
	p.Name = cleanString(p.Name)

	p.Education = cleanEducation(p.Education)
	p.Projects = cleanProjects(p.Projects)
	p.Work = cleanWork(p.Work)
	p.Skills = cleanSkills(p.Skills)

	return p
}

func cleanString(s string) string {
	s = strings.TrimSpace(s)
	return truncate(s, maxFieldRunes)
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

// cleanDetails 清洗要点列表：去空、去重、截断、限量。
func cleanDetails(in []string) []string {
	if len(in) == 0 {
		return nil
	}

	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))

	for _, d := range in {
		d = cleanString(d)
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
		if len(out) >= maxDetails {
			break
		}
	}

	if len(out) == 0 {
		return nil
	}
	return out
}

func cleanEducation(in []Education) []Education {
	out := make([]Education, 0, len(in))
	for _, e := range in {
		e.School = cleanString(e.School)
		e.Major = cleanString(e.Major)
		e.Degree = cleanString(e.Degree)
		e.Period = cleanString(e.Period)

		// 整条都是空的就丢掉，别让空对象占位
		if e.School == "" && e.Major == "" && e.Degree == "" && e.Period == "" {
			continue
		}
		out = append(out, e)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func cleanProjects(in []Project) []Project {
	out := make([]Project, 0, len(in))
	for _, p := range in {
		p.Name = cleanString(p.Name)
		p.Role = cleanString(p.Role)
		p.Details = cleanDetails(p.Details)

		if p.Name == "" && p.Role == "" && len(p.Details) == 0 {
			continue
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func cleanWork(in []Work) []Work {
	out := make([]Work, 0, len(in))
	for _, w := range in {
		w.Company = cleanString(w.Company)
		w.Title = cleanString(w.Title)
		w.Period = cleanString(w.Period)
		w.Details = cleanDetails(w.Details)

		if w.Company == "" && w.Title == "" && len(w.Details) == 0 {
			continue
		}
		out = append(out, w)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func cleanSkills(in []string) []string {
	if len(in) == 0 {
		return nil
	}

	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))

	for _, s := range in {
		s = cleanString(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
		if len(out) >= maxSkills {
			break
		}
	}

	if len(out) == 0 {
		return nil
	}
	return out
}
