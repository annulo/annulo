// 只注册对话里画图要用的部分（柱状、折线），ChatChart 按需 import 这个文件，不进主包
import { BarController, BarElement, CategoryScale, Chart, Legend, LinearScale, LineController, LineElement, PointElement, Tooltip } from 'chart.js'

Chart.register(BarController, BarElement, LineController, LineElement, PointElement, CategoryScale, LinearScale, Tooltip, Legend)

export { Chart }
