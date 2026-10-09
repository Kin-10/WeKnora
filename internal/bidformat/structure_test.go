package bidformat

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDetectStructureSplitsHandanMultiLotTender(t *testing.T) {
	text := `邯郸市中心血站酶免试剂盒采购项目招标文件
本项目共分为4个包，投标人可兼投不可兼中。
第1包：乙型肝炎病毒诊断试剂和丙型肝炎病毒诊断试剂，预算40.7万元
第2包：梅毒螺旋体抗体诊断试剂和人类免疫缺陷病毒抗原抗体检测试剂盒，预算47.85万元
第3包：乙型肝炎病毒诊断试剂和梅毒螺旋体抗体诊断试剂，预算20.9万元
第4包：丙型肝炎病毒诊断试剂和人类免疫缺陷病毒抗原抗体检测试剂盒，预算67.65万元
投标文件由商务标和技术标两部分组成，商务标"明标"、技术标"暗标"分开编制，技术标中不得出现供应商名称。`
	structure := DetectStructure(text)
	require.True(t, structure.HasChoice())
	require.True(t, structure.SeparateVolumes)
	require.True(t, structure.AnonymousTechnical)
	require.Len(t, structure.Lots, 4)
	require.Equal(t, "第1包", structure.Lots[0].Label)
	require.Contains(t, structure.Lots[0].Detail, "乙型肝炎病毒诊断试剂")
	require.Contains(t, structure.Lots[3].Detail, "丙型肝炎病毒诊断试剂")
}

func TestDetectStructureSeparateVolumesWithoutLots(t *testing.T) {
	text := `保定市中心血站采购项目招标文件
商务标（明标）与技术标（暗标）两册分别编制、分别加密上传，不得混编。本项目不分包。`
	structure := DetectStructure(text)
	require.True(t, structure.SeparateVolumes)
	require.True(t, structure.AnonymousTechnical)
	require.Empty(t, structure.Lots)
	require.True(t, structure.HasChoice())
}

func TestDetectStructureIgnoresContractLotReferencesWithoutMarker(t *testing.T) {
	text := `恩施州中心血站询价文件响应资料
业绩表中列有"包2/包3/包7"等历史合同，均属其他项目，不是本项目包段。本项目为单一分册。`
	structure := DetectStructure(text)
	require.False(t, structure.HasChoice())
	require.Empty(t, structure.Lots)
}

func TestDetectStructureSynthesizesLotsFromStatedTotal(t *testing.T) {
	text := `某采购项目招标文件：本项目分为三个包，评标按包依次进行。技术标与商务标分开制作。`
	structure := DetectStructure(text)
	require.Len(t, structure.Lots, 3)
	require.Equal(t, "第1包", structure.Lots[0].Label)
	require.Empty(t, structure.Lots[0].Detail)
}

func TestDetectStructureRejectsNonTenderText(t *testing.T) {
	structure := DetectStructure("年度工作总结报告：第一包干责任制成效显著，商务费用下降。")
	require.False(t, structure.HasChoice())
	require.Empty(t, structure.Lots)
}

func TestDetectStructureDeduplicatesRepeatedLotMarkers(t *testing.T) {
	text := `招标文件：第1包试剂；第2包耗材。评分办法：第1包报价30分，第2包报价30分。`
	structure := DetectStructure(text)
	require.Len(t, structure.Lots, 2)
}
